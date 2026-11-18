//go:build !windows

package socksconn

func classifyPlatformUDPError(error) (string, bool) {
	return "", false
}
