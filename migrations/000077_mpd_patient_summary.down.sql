DELETE FROM mpd_adapter_receipts WHERE operation='patient_summary';
ALTER TABLE mpd_adapter_receipts DROP CONSTRAINT mpd_adapter_receipts_operation_check;
ALTER TABLE mpd_adapter_receipts ADD CONSTRAINT mpd_adapter_receipts_operation_check
  CHECK (operation IN ('password','publication'));
ALTER TABLE mpd_video_state DROP CONSTRAINT mpd_patient_summary_version;
ALTER TABLE mpd_video_state DROP COLUMN patient_summary_media_version;
ALTER TABLE mpd_video_state DROP COLUMN patient_summary;
