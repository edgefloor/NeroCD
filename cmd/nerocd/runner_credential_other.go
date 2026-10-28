//go:build !unix

package main

import "errors"

func readRunnerCredentialFile(string) (string, error) {
	return "", errors.New("runner credential files require a supported Unix platform")
}

type runnerEnrollmentFiles struct{ token, credential, requestID string }

func prepareRunnerEnrollmentFiles(string, string) (*runnerEnrollmentFiles, error) {
	return nil, errors.New("runner enrollment files require a supported Unix platform")
}
func (*runnerEnrollmentFiles) close() {}
func (*runnerEnrollmentFiles) remove() error {
	return errors.New("runner enrollment files require a supported Unix platform")
}
