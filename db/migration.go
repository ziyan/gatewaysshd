package db

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/ziyan/gatewaysshd/db/migrations"
)

// database model for migration records
type migrationModel struct {
	ID string `gorm:"primary_key:true;size:256"`

	MigratedAt time.Time
	ReverseSQL string `gorm:"type:text"`
}

func (self *migrationModel) TableName() string {
	return "migration"
}

func (self *database) Migrate(ctx context.Context) error {
	if err := self.db.WithContext(ctx).AutoMigrate(&migrationModel{}); err != nil {
		log.Errorf("failed to migrate database: %s", err)
		return err
	}
	var existingModels []migrationModel
	if err := self.db.WithContext(ctx).Find(&existingModels).Error; err != nil {
		log.Errorf("failed to query for migrations: %s", err)
		return err
	}
	existingModelsMap := make(map[string]migrationModel)
	for _, migrationModel := range existingModels {
		existingModelsMap[migrationModel.ID] = migrationModel
	}

	currentMigrations := migrations.Migrations()
	currentMigrationIds := make(map[string]struct{}, len(currentMigrations))
	for _, migration := range currentMigrations {
		currentMigrationIds[migration.ID] = struct{}{}
	}

	// migrations recorded in the database but no longer known to this binary
	// were applied by a newer version, revert them in reverse order
	var unknownMigrationIds []string
	for migrationId := range existingModelsMap {
		if _, ok := currentMigrationIds[migrationId]; !ok {
			unknownMigrationIds = append(unknownMigrationIds, migrationId)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(unknownMigrationIds)))
	for _, migrationId := range unknownMigrationIds {
		model := existingModelsMap[migrationId]
		if strings.TrimSpace(model.ReverseSQL) == "" {
			panic(fmt.Sprintf("db: missing reverse sql for migration %s", migrationId))
		}
		log.Debugf("reverting database migration: %s", migrationId)
		if err := self.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec(model.ReverseSQL).Error; err != nil {
				return err
			}
			if err := tx.Where("id = ?", migrationId).Delete(&migrationModel{}).Error; err != nil {
				return err
			}
			return nil
		}); err != nil {
			log.Errorf("failed to revert database migration: %s: %s", migrationId, err)
			return err
		}
		log.Noticef("reverted database migration: %s", migrationId)
	}

	for _, migration := range currentMigrations {
		if existingModel, ok := existingModelsMap[migration.ID]; ok {
			log.Debugf("database migration %s already done at %s", migration.ID, existingModel.MigratedAt)
			continue
		}
		log.Debugf("migrating database: %s", migration.ID)
		if err := self.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if migration.SQL != "" {
				if err := tx.Exec(migration.SQL).Error; err != nil {
					return err
				}
			}
			if err := tx.Create(&migrationModel{
				ID:         migration.ID,
				MigratedAt: time.Now().In(time.Local),
				ReverseSQL: migration.ReverseSQL,
			}).Error; err != nil {
				return err
			}
			return nil
		}); err != nil {
			log.Errorf("failed to migrate database: %s: %s", migration.ID, err)
			return err
		}
		log.Noticef("migrated database: %s", migration.ID)
	}
	return nil
}
