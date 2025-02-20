package service

import (
	"bufio"
	"context"
	"fmt"
	"github.com/1Panel-dev/1Panel/backend/app/dto"
	"github.com/1Panel-dev/1Panel/backend/app/model"
	"github.com/1Panel-dev/1Panel/backend/buserr"
	"github.com/1Panel-dev/1Panel/backend/constant"
	"github.com/1Panel-dev/1Panel/backend/global"
	"github.com/1Panel-dev/1Panel/backend/utils/cmd"
	"github.com/1Panel-dev/1Panel/backend/utils/compose"
	"github.com/1Panel-dev/1Panel/backend/utils/encrypt"
	"github.com/1Panel-dev/1Panel/backend/utils/postgresql"
	"github.com/1Panel-dev/1Panel/backend/utils/postgresql/client"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jinzhu/copier"
	"github.com/pkg/errors"
	"os"
	"path"
)

type PostgresqlService struct{}

type IPostgresqlService interface {
	SearchWithPage(search dto.PostgresqlDBSearch) (int64, interface{}, error)
	ListDBOption() ([]dto.PostgresqlOption, error)
	Create(ctx context.Context, req dto.PostgresqlDBCreate) (*model.DatabasePostgresql, error)
	LoadFromRemote(req dto.PostgresqlLoadDB) error
	ChangeAccess(info dto.ChangeDBInfo) error
	ChangePassword(info dto.ChangeDBInfo) error
	UpdateVariables(req dto.PostgresqlVariablesUpdate) error
	UpdateConfByFile(info dto.PostgresqlConfUpdateByFile) error
	UpdateDescription(req dto.UpdateDescription) error
	DeleteCheck(req dto.PostgresqlDBDeleteCheck) ([]string, error)
	Delete(ctx context.Context, req dto.PostgresqlDBDelete) error

	LoadStatus(req dto.OperationWithNameAndType) (*dto.PostgresqlStatus, error)
	LoadVariables(req dto.OperationWithNameAndType) (*dto.PostgresqlVariables, error)
	LoadBaseInfo(req dto.OperationWithNameAndType) (*dto.DBBaseInfo, error)
	LoadRemoteAccess(req dto.OperationWithNameAndType) (bool, error)

	LoadDatabaseFile(req dto.OperationWithNameAndType) (string, error)
}

func NewIPostgresqlService() IPostgresqlService {
	return &PostgresqlService{}
}

func (u *PostgresqlService) SearchWithPage(search dto.PostgresqlDBSearch) (int64, interface{}, error) {
	total, postgresqls, err := postgresqlRepo.Page(search.Page, search.PageSize,
		postgresqlRepo.WithByPostgresqlName(search.Database),
		commonRepo.WithLikeName(search.Info),
		commonRepo.WithOrderRuleBy(search.OrderBy, search.Order),
	)
	var dtoPostgresqls []dto.PostgresqlDBInfo
	for _, pg := range postgresqls {
		var item dto.PostgresqlDBInfo
		if err := copier.Copy(&item, &pg); err != nil {
			return 0, nil, errors.WithMessage(constant.ErrStructTransform, err.Error())
		}
		dtoPostgresqls = append(dtoPostgresqls, item)
	}
	return total, dtoPostgresqls, err
}

func (u *PostgresqlService) ListDBOption() ([]dto.PostgresqlOption, error) {
	postgresqls, err := postgresqlRepo.List()
	if err != nil {
		return nil, err
	}

	databases, err := databaseRepo.GetList(databaseRepo.WithTypeList("postgresql,mariadb"))
	if err != nil {
		return nil, err
	}
	var dbs []dto.PostgresqlOption
	for _, pg := range postgresqls {
		var item dto.PostgresqlOption
		if err := copier.Copy(&item, &pg); err != nil {
			return nil, errors.WithMessage(constant.ErrStructTransform, err.Error())
		}
		item.Database = pg.PostgresqlName
		for _, database := range databases {
			if database.Name == item.Database {
				item.Type = database.Type
			}
		}
		dbs = append(dbs, item)
	}
	return dbs, err
}

func (u *PostgresqlService) Create(ctx context.Context, req dto.PostgresqlDBCreate) (*model.DatabasePostgresql, error) {
	if cmd.CheckIllegal(req.Name, req.Username, req.Password, req.Format) {
		return nil, buserr.New(constant.ErrCmdIllegal)
	}

	pgsql, _ := postgresqlRepo.Get(commonRepo.WithByName(req.Name), postgresqlRepo.WithByPostgresqlName(req.Database), databaseRepo.WithByFrom(req.From))
	if pgsql.ID != 0 {
		return nil, constant.ErrRecordExist
	}

	var createItem model.DatabasePostgresql
	if err := copier.Copy(&createItem, &req); err != nil {
		return nil, errors.WithMessage(constant.ErrStructTransform, err.Error())
	}

	if req.From == "local" && req.Username == "root" {
		return nil, errors.New("Cannot set root as user name")
	}

	cli, version, err := LoadPostgresqlClientByFrom(req.Database)
	if err != nil {
		return nil, err
	}
	createItem.PostgresqlName = req.Database
	defer cli.Close()
	if err := cli.Create(client.CreateInfo{
		Name:     req.Name,
		Format:   req.Format,
		Username: req.Username,
		Password: req.Password,
		Version:  version,
		Timeout:  300,
	}); err != nil {
		return nil, err
	}

	global.LOG.Infof("create database %s successful!", req.Name)
	if err := postgresqlRepo.Create(ctx, &createItem); err != nil {
		return nil, err
	}
	return &createItem, nil
}
func LoadPostgresqlClientByFrom(database string) (postgresql.PostgresqlClient, string, error) {
	var (
		dbInfo  client.DBInfo
		version string
		err     error
	)

	dbInfo.Timeout = 300
	databaseItem, err := databaseRepo.Get(commonRepo.WithByName(database))
	if err != nil {
		return nil, "", err
	}
	dbInfo.From = databaseItem.From
	dbInfo.Database = database
	if dbInfo.From != "local" {
		dbInfo.Address = databaseItem.Address
		dbInfo.Port = databaseItem.Port
		dbInfo.Username = databaseItem.Username
		dbInfo.Password = databaseItem.Password
		dbInfo.SSL = databaseItem.SSL
		dbInfo.ClientKey = databaseItem.ClientKey
		dbInfo.ClientCert = databaseItem.ClientCert
		dbInfo.RootCert = databaseItem.RootCert
		dbInfo.SkipVerify = databaseItem.SkipVerify
		version = databaseItem.Version

	} else {
		app, err := appInstallRepo.LoadBaseInfo(databaseItem.Type, database)
		if err != nil {
			return nil, "", err
		}
		dbInfo.From = "local"
		dbInfo.Address = app.ContainerName
		dbInfo.Username = app.UserName
		dbInfo.Password = app.Password
		dbInfo.Port = uint(app.Port)
	}

	cli, err := postgresql.NewPostgresqlClient(dbInfo)
	if err != nil {
		return nil, "", err
	}
	return cli, version, nil
}
func (u *PostgresqlService) LoadFromRemote(req dto.PostgresqlLoadDB) error {

	return nil
}

func (u *PostgresqlService) UpdateDescription(req dto.UpdateDescription) error {
	return postgresqlRepo.Update(req.ID, map[string]interface{}{"description": req.Description})
}

func (u *PostgresqlService) DeleteCheck(req dto.PostgresqlDBDeleteCheck) ([]string, error) {
	var appInUsed []string
	db, err := postgresqlRepo.Get(commonRepo.WithByID(req.ID))
	if err != nil {
		return appInUsed, err
	}

	if db.From == "local" {
		app, err := appInstallRepo.LoadBaseInfo(req.Type, req.Database)
		if err != nil {
			return appInUsed, err
		}
		apps, _ := appInstallResourceRepo.GetBy(appInstallResourceRepo.WithLinkId(app.ID), appInstallResourceRepo.WithResourceId(db.ID))
		for _, app := range apps {
			appInstall, _ := appInstallRepo.GetFirst(commonRepo.WithByID(app.AppInstallId))
			if appInstall.ID != 0 {
				appInUsed = append(appInUsed, appInstall.Name)
			}
		}
	} else {
		apps, _ := appInstallResourceRepo.GetBy(appInstallResourceRepo.WithResourceId(db.ID))
		for _, app := range apps {
			appInstall, _ := appInstallRepo.GetFirst(commonRepo.WithByID(app.AppInstallId))
			if appInstall.ID != 0 {
				appInUsed = append(appInUsed, appInstall.Name)
			}
		}
	}

	return appInUsed, nil
}

func (u *PostgresqlService) Delete(ctx context.Context, req dto.PostgresqlDBDelete) error {
	db, err := postgresqlRepo.Get(commonRepo.WithByID(req.ID))
	if err != nil && !req.ForceDelete {
		return err
	}
	cli, version, err := LoadPostgresqlClientByFrom(req.Database)
	if err != nil {
		return err
	}
	defer cli.Close()
	if err := cli.Delete(client.DeleteInfo{
		Name:       db.Name,
		Version:    version,
		Username:   db.Username,
		Permission: "",
		Timeout:    300,
	}); err != nil && !req.ForceDelete {
		return err
	}

	if req.DeleteBackup {
		uploadDir := path.Join(global.CONF.System.BaseDir, fmt.Sprintf("1panel/uploads/database/%s/%s/%s", req.Type, req.Database, db.Name))
		if _, err := os.Stat(uploadDir); err == nil {
			_ = os.RemoveAll(uploadDir)
		}
		localDir, err := loadLocalDir()
		if err != nil && !req.ForceDelete {
			return err
		}
		backupDir := path.Join(localDir, fmt.Sprintf("database/%s/%s/%s", req.Type, db.PostgresqlName, db.Name))
		if _, err := os.Stat(backupDir); err == nil {
			_ = os.RemoveAll(backupDir)
		}
		_ = backupRepo.DeleteRecord(ctx, commonRepo.WithByType(req.Type), commonRepo.WithByName(req.Database), backupRepo.WithByDetailName(db.Name))
		global.LOG.Infof("delete database %s-%s backups successful", req.Database, db.Name)
	}

	_ = postgresqlRepo.Delete(ctx, commonRepo.WithByID(db.ID))
	return nil
}

func (u *PostgresqlService) ChangePassword(req dto.ChangeDBInfo) error {
	if cmd.CheckIllegal(req.Value) {
		return buserr.New(constant.ErrCmdIllegal)
	}
	cli, version, err := LoadPostgresqlClientByFrom(req.Database)
	if err != nil {
		return err
	}
	defer cli.Close()
	var (
		postgresqlData model.DatabasePostgresql
		passwordInfo   client.PasswordChangeInfo
	)
	passwordInfo.Password = req.Value
	passwordInfo.Timeout = 300
	passwordInfo.Version = version

	if req.ID != 0 {
		postgresqlData, err = postgresqlRepo.Get(commonRepo.WithByID(req.ID))
		if err != nil {
			return err
		}
		passwordInfo.Name = postgresqlData.Name
		passwordInfo.Username = postgresqlData.Username
	} else {
		dbItem, err := databaseRepo.Get(commonRepo.WithByType(req.Type), commonRepo.WithByFrom(req.From))
		if err != nil {
			return err
		}
		passwordInfo.Username = dbItem.Username
	}
	if err := cli.ChangePassword(passwordInfo); err != nil {
		return err
	}

	if req.ID != 0 {
		var appRess []model.AppInstallResource
		if req.From == "local" {
			app, err := appInstallRepo.LoadBaseInfo(req.Type, req.Database)
			if err != nil {
				return err
			}
			appRess, _ = appInstallResourceRepo.GetBy(appInstallResourceRepo.WithLinkId(app.ID), appInstallResourceRepo.WithResourceId(postgresqlData.ID))
		} else {
			appRess, _ = appInstallResourceRepo.GetBy(appInstallResourceRepo.WithResourceId(postgresqlData.ID))
		}
		for _, appRes := range appRess {
			appInstall, err := appInstallRepo.GetFirst(commonRepo.WithByID(appRes.AppInstallId))
			if err != nil {
				return err
			}
			appModel, err := appRepo.GetFirst(commonRepo.WithByID(appInstall.AppId))
			if err != nil {
				return err
			}

			global.LOG.Infof("start to update postgresql password used by app %s-%s", appModel.Key, appInstall.Name)
			if err := updateInstallInfoInDB(appModel.Key, appInstall.Name, "user-password", true, req.Value); err != nil {
				return err
			}
		}
		global.LOG.Info("execute password change sql successful")
		pass, err := encrypt.StringEncrypt(req.Value)
		if err != nil {
			return fmt.Errorf("decrypt database db password failed, err: %v", err)
		}
		_ = postgresqlRepo.Update(postgresqlData.ID, map[string]interface{}{"password": pass})
		return nil
	}

	if err := updateInstallInfoInDB(req.Type, req.Database, "password", false, req.Value); err != nil {
		return err
	}
	return nil
}

func (u *PostgresqlService) ChangeAccess(req dto.ChangeDBInfo) error {
	if cmd.CheckIllegal(req.Value) {
		return buserr.New(constant.ErrCmdIllegal)
	}
	cli, version, err := LoadPostgresqlClientByFrom(req.Database)
	if err != nil {
		return err
	}
	defer cli.Close()
	var (
		postgresqlData model.DatabasePostgresql
		accessInfo     client.AccessChangeInfo
	)
	accessInfo.Permission = req.Value
	accessInfo.Timeout = 300
	accessInfo.Version = version

	if req.ID != 0 {
		postgresqlData, err = postgresqlRepo.Get(commonRepo.WithByID(req.ID))
		if err != nil {
			return err
		}
		accessInfo.Name = postgresqlData.Name
		accessInfo.Username = postgresqlData.Username
		accessInfo.Password = postgresqlData.Password
	} else {
		accessInfo.Username = "root"
	}
	if err := cli.ChangeAccess(accessInfo); err != nil {
		return err
	}

	if postgresqlData.ID != 0 {
		_ = postgresqlRepo.Update(postgresqlData.ID, map[string]interface{}{"permission": req.Value})
	}

	return nil
}

func (u *PostgresqlService) UpdateConfByFile(req dto.PostgresqlConfUpdateByFile) error {
	app, err := appInstallRepo.LoadBaseInfo(req.Type, req.Database)
	if err != nil {
		return err
	}
	conf := fmt.Sprintf("%s/%s/%s/data/postgresql.conf", constant.AppInstallDir, req.Type, app.Name)
	file, err := os.OpenFile(conf, os.O_WRONLY|os.O_TRUNC, 0640)
	if err != nil {
		return err
	}
	defer file.Close()
	write := bufio.NewWriter(file)
	_, _ = write.WriteString(req.File)
	write.Flush()
	cli, _, err := LoadPostgresqlClientByFrom(req.Database)
	if err != nil {
		return err
	}
	defer cli.Close()
	if _, err := compose.Restart(fmt.Sprintf("%s/%s/%s/docker-compose.yml", constant.AppInstallDir, req.Type, app.Name)); err != nil {
		return err
	}
	return nil
}

func (u *PostgresqlService) UpdateVariables(req dto.PostgresqlVariablesUpdate) error {

	return nil
}

func (u *PostgresqlService) LoadBaseInfo(req dto.OperationWithNameAndType) (*dto.DBBaseInfo, error) {
	var data dto.DBBaseInfo
	app, err := appInstallRepo.LoadBaseInfo(req.Type, req.Name)
	if err != nil {
		return nil, err
	}
	data.ContainerName = app.ContainerName
	data.Name = app.Name
	data.Port = int64(app.Port)

	return &data, nil
}

func (u *PostgresqlService) LoadRemoteAccess(req dto.OperationWithNameAndType) (bool, error) {

	return true, nil
}

func (u *PostgresqlService) LoadVariables(req dto.OperationWithNameAndType) (*dto.PostgresqlVariables, error) {

	return nil, nil
}

func (u *PostgresqlService) LoadStatus(req dto.OperationWithNameAndType) (*dto.PostgresqlStatus, error) {
	app, err := appInstallRepo.LoadBaseInfo(req.Type, req.Name)
	if err != nil {
		return nil, err
	}
	cli, _, err := LoadPostgresqlClientByFrom(app.Name)
	if err != nil {
		return nil, err
	}
	defer cli.Close()
	status := cli.Status()
	postgresqlStatus := dto.PostgresqlStatus{}
	copier.Copy(&postgresqlStatus,&status)
	return &postgresqlStatus, nil
}

func (u *PostgresqlService) LoadDatabaseFile(req dto.OperationWithNameAndType) (string, error) {
	filePath := ""
	switch req.Type {
	case "postgresql-conf":
		filePath = path.Join(global.CONF.System.DataDir, fmt.Sprintf("apps/postgresql/%s/data/postgresql.conf", req.Name))
	}
	if _, err := os.Stat(filePath); err != nil {
		return "", buserr.New("ErrHttpReqNotFound")
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	return string(content), nil
}
