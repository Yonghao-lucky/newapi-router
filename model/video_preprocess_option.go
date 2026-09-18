package model

import "github.com/QuantumNous/new-api/setting/video_preprocess_setting"

// SaveVideoPreprocessSettingsDocument persists the Video Preprocessing settings
// document.
//
// It deliberately does not go through UpdateOption. The caller
// (video_preprocess_setting.Save) already holds the settings save lock, has
// validated the whole document, and publishes the snapshot itself. Routing
// through updateOptionMap would republish under that same lock, and the
// per-field validation it performs is meaningless for a document that is always
// written whole.
//
// For the same reason this function does not touch common.OptionMap: the caller
// refreshes it once it has released the save lock, so the option-map lock is
// never taken while the save lock is held.
//
// The write is a single row in the existing Option table, so the stored state
// is never a partial candidate.
func SaveVideoPreprocessSettingsDocument(value string) error {
	key := video_preprocess_setting.SettingsOptionKey
	option := Option{Key: key}
	if err := DB.FirstOrCreate(&option, Option{Key: key}).Error; err != nil {
		return err
	}
	option.Value = value
	return DB.Save(&option).Error
}
