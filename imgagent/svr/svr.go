package svr

import (
	"errors"
	"io"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"imgagent/bailian"
	"imgagent/db"
	"imgagent/pkg/dbutil"
	"imgagent/pkg/middleware"
	"imgagent/storage"
)

type Config struct {
	APIVersion     string         `json:"api_version"`
	Temp           string         `json:"temp"`
	Storage        storage.Config `json:"storage"`
	DB             dbutil.Config  `json:"db"`
	Redis          RedisConfig    `json:"redis"`
	BailianConfig  bailian.Config `json:"-"` // 从外部传入
	DocumentConfig DocumentConfig `json:"-"` // 从外部传入
}

type RedisConfig struct {
	DisableCluster bool     `json:"disable_cluster"`
	ExpireSecs     int      `json:"expire_secs"`
	Addrs          []string `json:"addrs"`
	MasterName     string   `json:"master_name,omitempty"`
	SentinelAddrs  []string `json:"sentinel_addrs,omitempty"`
}

type EmbeddingConfig struct {
	URL    string `json:"url"`
	Model  string `json:"model"`
	APIKey string `json:"api_key"`
}

type Service struct {
	conf          Config
	db            db.IDataBase
	redis         redis.UniversalClient
	stg           *storage.Storage
	bailianClient *bailian.Client
	documentMgr   *DocumentMgr
}

func New(conf Config, bailianClient *bailian.Client) (*Service, error) {
	if conf.Temp == "" {
		conf.Temp = "./temp"
	}
	err := os.MkdirAll(conf.Temp, 0776)
	if err != nil {
		zap.S().Errorf("Failed to mkdir, err: %v", err)
		return nil, err
	}

	stg, err := storage.NewStorage(conf.Storage)
	if err != nil {
		zap.S().Errorf("Failed to new storage, err: %v", err)
		return nil, err
	}
	db, err := db.NewDatabase(conf.DB)
	if err != nil {
		zap.S().Errorf("Failed to new database, err: %v", err)
		return nil, err
	}

	if len(conf.Redis.Addrs) == 0 {
		return nil, errors.New("invalid addrs")
	}
	var redisCli redis.UniversalClient
	if conf.Redis.ExpireSecs == 0 {
		conf.Redis.ExpireSecs = 120
	}
	if conf.Redis.DisableCluster {
		redisCli = redis.NewClient(&redis.Options{
			Addr: conf.Redis.Addrs[0],
		})
	} else {
		redisCli = redis.NewClusterClient(&redis.ClusterOptions{
			Addrs: conf.Redis.Addrs,
		})
	}

	// 创建文档管理器
	var docMgr *DocumentMgr
	if conf.DocumentConfig.Enable {
		confEx := DocumentConfigEx{
			config: conf.DocumentConfig,
			db:     db,
		}
		var err error
		docMgr, err = newDocumentMgr(confEx, bailianClient)
		if err != nil {
			zap.S().Errorf("Failed to new document manager, err: %v", err)
			return nil, err
		}
		// 启动文档管理器
		docMgr.Run()
		zap.S().Info("Document manager started")
	}

	return &Service{
		conf:          conf,
		db:            db,
		redis:         redisCli,
		stg:           stg,
		bailianClient: bailianClient,
		documentMgr:   docMgr,
	}, nil
}

func (s *Service) RegisterRouter(writer io.Writer) *gin.Engine {
	router := middleware.NewRouter(writer)
	api := router.Group(s.conf.APIVersion)
	authGroup := api.Group("")
	// 暂不需要 auth
	authGroup.Use(s.NilAuth())

	// Document
	authGroup.POST("/documents", s.HandleCreateDocument)
	authGroup.GET("/documents/:document_id", s.HandleGetDocument)
	authGroup.PUT("/documents/:document_id", s.HandleUpdateDocument)
	authGroup.DELETE("/documents/:document_id", s.HandleDeleteDocument)
	authGroup.GET("/documents", s.HandleListDocuments)

	// Chapter
	authGroup.GET("/documents/:document_id/chapters/:id", s.HandleGetChapter)
	authGroup.PUT("/documents/:document_id/chapters/:id", s.HandleUpdateChapter)
	authGroup.DELETE("/documents/:document_id/chapters/:id", s.HandleDeleteChapter)
	authGroup.GET("/documents/:document_id/chapters", s.HandleListChapters)

	// Role
	authGroup.GET("/documents/:document_id/roles", s.HandleGetRoles)

	// Scene
	authGroup.GET("/documents/:document_id/scenes", s.HandleListScenesByDocument)
	authGroup.GET("/chapters/:chapter_id/scenes", s.HandleListScenesByChapter)

	return router
}
