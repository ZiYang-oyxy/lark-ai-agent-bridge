package credentials

import (
	"fmt"
	"os"
	"strings"
	"syscall"
)

type Credentials struct {
	AppID     string
	AppSecret string
}

func Load() (Credentials, error) {
	appID, err := loadValue([]string{"LAB_LARK_APP_ID", "LARK_APP_ID"}, []string{"LAB_LARK_APP_ID_FILE", "LARK_APP_ID_FILE"})
	if err != nil {
		return Credentials{}, fmt.Errorf("load App ID: %w", err)
	}
	appSecret, err := loadValue([]string{"LAB_LARK_APP_SECRET", "LARK_APP_SECRET"}, []string{"LAB_LARK_APP_SECRET_FILE", "LARK_APP_SECRET_FILE"})
	if err != nil {
		return Credentials{}, fmt.Errorf("load App secret: %w", err)
	}
	if appID == "" || appSecret == "" {
		return Credentials{}, fmt.Errorf("Feishu App ID and App secret are required")
	}
	return Credentials{AppID: appID, AppSecret: appSecret}, nil
}

func loadValue(valueNames, fileNames []string) (string, error) {
	for _, name := range valueNames {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value, nil
		}
	}
	for _, name := range fileNames {
		if path := strings.TrimSpace(os.Getenv(name)); path != "" {
			return readPrivateFile(path)
		}
	}
	return "", nil
}

func readPrivateFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("credential file is not a regular file: %s", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("credential file permissions must deny group and other access: %s", path)
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Geteuid() {
		return "", fmt.Errorf("credential file must be owned by the current user: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimRight(string(data), "\r\n")
	if value == "" {
		return "", fmt.Errorf("credential file is empty: %s", path)
	}
	return value, nil
}
