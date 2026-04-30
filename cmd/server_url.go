package cmd

import "os"

func resolveServerURL(explicit string) string {
	if explicit != "" {
		return explicit
	}
	return os.Getenv("DITTO_SERVER")
}
