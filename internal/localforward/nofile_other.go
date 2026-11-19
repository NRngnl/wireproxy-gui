//go:build !unix

package localforward

func processFDLimit() int {
	return 0
}
