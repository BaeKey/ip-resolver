package provider

import (
    "fmt"
    "ip-resolver/internal/monitor"
)

func NewProviderByName(name, secretID, secretKey string, mon *monitor.Monitor) (IPProvider, error) {
	switch name {
	case "38599":
		return New_38599_Provider(secretID, secretKey, mon), nil
	case "30498":
		return New_30498_Provider(secretID, secretKey, mon), nil
	default:
		return nil, fmt.Errorf("未知供应商: %s", name)
	}
}