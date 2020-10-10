package db

import (
	"time"

	"github.com/jinzhu/gorm"

	"github.com/ziyan/gatewaysshd/db/migrations"
)

// database model for migration records
type Migration struct {
	ID string `gorm:"primary_key:true"`

	Migrated time.Time
}

func (self *Migration) TableName() string {
	return "migration"
}

func (self *database) Migrate() error {
	if err := self.db.AutoMigrate(&Migration{}).Error; err != nil {
		log.Errorf("failed to migrate database: %s", err)
		return err
	}
	var existingModels []Migration
	if err := self.db.Find(&existingModels).Error; err != nil {
		log.Errorf("failed to query for migrations: %s", err)
		return err
	}
	existingModelsMap := make(map[string]Migration)
	for _, Migration := range existingModels {
		existingModelsMap[Migration.ID] = Migration
	}

	for _, migration := range migrations.Migrations() {
		if existingModel, ok := existingModelsMap[migration.ID]; ok {
			log.Debugf("database migration %s already done at %s", migration.ID, existingModel.Migrated)
			continue
		}
		log.Debugf("migrating database: %s", migration.ID)
		if err := self.db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec(migration.SQL).Error; err != nil {
				return err
			}
			if err := tx.Create(&Migration{
				ID:       migration.ID,
				Migrated: time.Now().In(time.Local),
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
