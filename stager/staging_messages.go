package stager

type StagingRequest struct {
	AppId           string           `json:"app_id"`
	TaskId          string           `json:"task_id"`
	Stack           string           `json:"stack"`
	DownloadUri     string           `json:"download_uri"`
	UploadUri       string           `json:"upload_uri"`
	MemoryMB        int              `json:"memoryMB"`
	DiskMB          int              `json:"diskMB"`
	AdminBuildpacks []AdminBuildpack `json:"admin_buildpacks"`
	Environment     [][]string       `json:"environment"`

	//	BuildpackCacheUploadUri   string                 `json:"buildpack_cache_upload_uri"`
	//	BuildpackCacheDownloadUri string                 `json:"buildpack_cache_download_uri"`
}

type StagingResult struct {
	DetectedBuildpack string `json:"detected_buildpack"`
}

type StagingResponse struct {
	DetectedBuildpack string `json:"detected_buildpack,omitempty"`

	Error string `json:"error,omitempty"`
}

type AdminBuildpack struct {
	Key string `json:"key"`
	Url string `json:"url"`
}
