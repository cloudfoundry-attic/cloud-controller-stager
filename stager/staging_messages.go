package stager

type StagingRequest struct {
	AppId  string `json:"app_id"`
	TaskId string `json:"task_id"`
	Stack  string `json:"stack"`

	//	Properties                map[string]interface{} `json:"properties"`
	//	BuildpackCacheUploadUri   string                 `json:"buildpack_cache_upload_uri"`
	//	BuildpackCacheDownloadUri string                 `json:"buildpack_cache_download_uri"`
	//	UploadUri                 string                 `json:"upload_uri"`
	//	DownloadUri               string                 `json:"download_uri"`
}

type StagingResponse struct {
	Error string `json:"error,omitempty"`
}
