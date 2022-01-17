package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bytebase/bytebase/common"

	// embed will embeds the acl policy.
	_ "embed"

	"github.com/bytebase/bytebase/api"
	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	scas "github.com/qiangmzsx/string-adapter/v2"
	"go.uber.org/zap"
)

// Server is the Bytebase server.
type Server struct {
	TaskScheduler      *TaskScheduler
	TaskCheckScheduler *TaskCheckScheduler
	SchemaSyncer       *SchemaSyncer
	BackupRunner       *BackupRunner
	AnomalyScanner     *AnomalyScanner

	ActivityManager *ActivityManager

	CacheService api.CacheService

	SettingService          api.SettingService
	PrincipalService        api.PrincipalService
	MemberService           api.MemberService
	PolicyService           api.PolicyService
	ProjectService          api.ProjectService
	ProjectMemberService    api.ProjectMemberService
	ProjectWebhookService   api.ProjectWebhookService
	EnvironmentService      api.EnvironmentService
	InstanceService         api.InstanceService
	InstanceUserService     api.InstanceUserService
	DatabaseService         api.DatabaseService
	TableService            api.TableService
	ColumnService           api.ColumnService
	ViewService             api.ViewService
	IndexService            api.IndexService
	DataSourceService       api.DataSourceService
	BackupService           api.BackupService
	IssueService            api.IssueService
	IssueSubscriberService  api.IssueSubscriberService
	PipelineService         api.PipelineService
	StageService            api.StageService
	TaskService             api.TaskService
	TaskCheckRunService     api.TaskCheckRunService
	ActivityService         api.ActivityService
	InboxService            api.InboxService
	BookmarkService         api.BookmarkService
	VCSService              api.VCSService
	RepositoryService       api.RepositoryService
	AnomalyService          api.AnomalyService
	LabelService            api.LabelService
	DeploymentConfigService api.DeploymentConfigService
	SavedQueryService       api.SavedQueryService

	e *echo.Echo

	l            *zap.Logger
	version      string
	mode         string
	host         string
	port         int
	frontendHost string
	frontendPort int
	startedTs    int64
	secret       string
	readonly     bool
	demo         bool
	plan         api.PlanType
	dataDir      string
}

//go:embed acl_casbin_model.conf
var casbinModel string

//go:embed acl_casbin_policy_owner.csv
var casbinOwnerPolicy string

//go:embed acl_casbin_policy_dba.csv
var casbinDBAPolicy string

//go:embed acl_casbin_policy_developer.csv
var casbinDeveloperPolicy string

// NewServer creates a server.
func NewServer(logger *zap.Logger, version string, host string, port int, frontendHost string, frontendPort int, mode string, dataDir string, backupRunnerInterval time.Duration, secret string, readonly bool, demo bool, debug bool) *Server {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

	// Disallow to be embedded in an iframe
	e.Use(middleware.SecureWithConfig(middleware.SecureConfig{
		XFrameOptions: "DENY",
	}))

	embedFrontend(logger, e)

	s := &Server{
		l:            logger,
		CacheService: NewCacheService(logger),
		e:            e,
		version:      version,
		mode:         mode,
		host:         host,
		port:         port,
		frontendHost: frontendHost,
		frontendPort: frontendPort,
		startedTs:    time.Now().Unix(),
		secret:       secret,
		readonly:     readonly,
		demo:         demo,
		plan:         api.TEAM,
		dataDir:      dataDir,
	}

	if !readonly {
		// Task scheduler
		taskScheduler := NewTaskScheduler(logger, s)

		defaultExecutor := NewDefaultTaskExecutor(logger)
		taskScheduler.Register(string(api.TaskGeneral), defaultExecutor)

		createDBExecutor := NewDatabaseCreateTaskExecutor(logger)
		taskScheduler.Register(string(api.TaskDatabaseCreate), createDBExecutor)

		schemaUpdateExecutor := NewSchemaUpdateTaskExecutor(logger)
		taskScheduler.Register(string(api.TaskDatabaseSchemaUpdate), schemaUpdateExecutor)

		dataUpdateExecutor := NewDataUpdateTaskExecutor(logger)
		taskScheduler.Register(string(api.TaskDatabaseDataUpdate), dataUpdateExecutor)

		backupDBExecutor := NewDatabaseBackupTaskExecutor(logger)
		taskScheduler.Register(string(api.TaskDatabaseBackup), backupDBExecutor)

		restoreDBExecutor := NewDatabaseRestoreTaskExecutor(logger)
		taskScheduler.Register(string(api.TaskDatabaseRestore), restoreDBExecutor)

		s.TaskScheduler = taskScheduler

		// Task check scheduler
		taskCheckScheduler := NewTaskCheckScheduler(logger, s)

		statementExecutor := NewTaskCheckStatementAdvisorExecutor(logger)
		taskCheckScheduler.Register(string(api.TaskCheckDatabaseStatementFakeAdvise), statementExecutor)
		taskCheckScheduler.Register(string(api.TaskCheckDatabaseStatementSyntax), statementExecutor)
		taskCheckScheduler.Register(string(api.TaskCheckDatabaseStatementCompatibility), statementExecutor)

		databaseConnectExecutor := NewTaskCheckDatabaseConnectExecutor(logger)
		taskCheckScheduler.Register(string(api.TaskCheckDatabaseConnect), databaseConnectExecutor)

		migrationSchemaExecutor := NewTaskCheckMigrationSchemaExecutor(logger)
		taskCheckScheduler.Register(string(api.TaskCheckInstanceMigrationSchema), migrationSchemaExecutor)

		timingExecutor := NewTaskCheckTimingExecutor(logger)
		taskCheckScheduler.Register(string(api.TaskCheckGeneralEarliestAllowedTime), timingExecutor)

		s.TaskCheckScheduler = taskCheckScheduler

		// Schema syncer
		s.SchemaSyncer = NewSchemaSyncer(logger, s)

		// Backup runner
		s.BackupRunner = NewBackupRunner(logger, s, backupRunnerInterval)

		// Anomaly scanner
		s.AnomalyScanner = NewAnomalyScanner(logger, s)
	}

	// Middleware
	if mode == "dev" || debug {
		e.Use(middleware.LoggerWithConfig(middleware.LoggerConfig{
			Skipper: func(c echo.Context) bool {
				return !common.HasPrefixes(c.Path(), "/api", "/hook")
			},
			Format: `{"time":"${time_rfc3339}",` +
				`"method":"${method}","uri":"${uri}",` +
				`"status":${status},"error":"${error}"}` + "\n",
		}))
	}
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return recoverMiddleware(logger, next)
	})

	webhookGroup := e.Group("/hook")
	s.registerWebhookRoutes(webhookGroup)

	apiGroup := e.Group("/api")

	apiGroup.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return JWTMiddleware(logger, s.PrincipalService, next, mode, secret)
	})

	m, err := model.NewModelFromString(casbinModel)
	if err != nil {
		e.Logger.Fatal(err)
	}
	sa := scas.NewAdapter(strings.Join([]string{casbinOwnerPolicy, casbinDBAPolicy, casbinDeveloperPolicy}, "\n"))
	ce, err := casbin.NewEnforcer(m, sa)
	if err != nil {
		e.Logger.Fatal(err)
	}
	apiGroup.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return aclMiddleware(logger, s, ce, next, readonly)
	})
	s.registerSettingRoutes(apiGroup)
	s.registerActuatorRoutes(apiGroup)
	s.registerAuthRoutes(apiGroup)
	s.registerPrincipalRoutes(apiGroup)
	s.registerMemberRoutes(apiGroup)
	s.registerPolicyRoutes(apiGroup)
	s.registerProjectRoutes(apiGroup)
	s.registerProjectWebhookRoutes(apiGroup)
	s.registerProjectMemberRoutes(apiGroup)
	s.registerEnvironmentRoutes(apiGroup)
	s.registerInstanceRoutes(apiGroup)
	s.registerDatabaseRoutes(apiGroup)
	s.registerIssueRoutes(apiGroup)
	s.registerIssueSubscriberRoutes(apiGroup)
	s.registerTaskRoutes(apiGroup)
	s.registerActivityRoutes(apiGroup)
	s.registerInboxRoutes(apiGroup)
	s.registerBookmarkRoutes(apiGroup)
	s.registerSQLRoutes(apiGroup)
	s.registerVCSRoutes(apiGroup)
	s.registerPlanRoutes(apiGroup)
	s.registerLabelRoutes(apiGroup)
	s.registerSavedQueryRoutes(apiGroup)

	allRoutes, err := json.MarshalIndent(e.Routes(), "", "  ")
	if err != nil {
		e.Logger.Fatal(err)
	}

	logger.Debug(fmt.Sprintf("All registered routes: %v", string(allRoutes)))

	return s
}

// Run will run the server.
func (server *Server) Run() error {
	if !server.readonly {
		if err := server.TaskScheduler.Run(); err != nil {
			return err
		}

		if err := server.TaskCheckScheduler.Run(); err != nil {
			return err
		}

		if err := server.SchemaSyncer.Run(); err != nil {
			return err
		}

		if err := server.BackupRunner.Run(); err != nil {
			return err
		}

		if err := server.AnomalyScanner.Run(); err != nil {
			return err
		}
	}

	// Sleep for 1 sec to make sure port is released between runs.
	time.Sleep(time.Duration(1) * time.Second)

	return server.e.Start(fmt.Sprintf(":%d", server.port))
}

// Shutdown will shut down the server.
func (server *Server) Shutdown(ctx context.Context) {
	if err := server.e.Shutdown(ctx); err != nil {
		server.e.Logger.Fatal(err)
	}
}
