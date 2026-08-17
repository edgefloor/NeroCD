//go:build !unix

package main

import "errors"

func readRunnerCredentialFile(string) (string, error) {
	return "", errors.New("runner credential files require a supported Unix platform")
}

func prepareRunnerEnrollmentFiles(string, string) (string, string, string, error) {
	return "", "", "", errors.New("runner enrollment files require a supported Unix platform")
}

func removeRunnerEnrollmentFile(string) error {
	return errors.New("runner enrollment files require a supported Unix platform")
}
