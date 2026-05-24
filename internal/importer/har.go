package importer

import (
	"encoding/json"
	"fmt"
	"os"
)

type harFile struct {
	Log harLog `json:"log"`
}

type harLog struct {
	Entries []harEntry `json:"entries"`
}

type harEntry struct {
	Request  harRequest  `json:"request"`
	Response harResponse `json:"response"`
}

type harRequest struct {
	Method   string       `json:"method"`
	URL      string       `json:"url"`
	Headers  []harHeader  `json:"headers"`
	PostData *harPostData `json:"postData,omitempty"`
}

type harHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type harPostData struct {
	MimeType string `json:"mimeType"`
	Text     string `json:"text"`
}

type harResponse struct {
	Status  int         `json:"status"`
	Headers []harHeader `json:"headers"`
	Content harContent  `json:"content"`
}

type harContent struct {
	MimeType string `json:"mimeType"`
	Text     string `json:"text"`
}

func parseHARFile(path string) (*harFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading HAR file: %w", err)
	}
	return parseHAR(data)
}

func parseHAR(data []byte) (*harFile, error) {
	var h harFile
	if err := json.Unmarshal(data, &h); err != nil {
		return nil, fmt.Errorf("parsing HAR: %w", err)
	}
	return &h, nil
}
