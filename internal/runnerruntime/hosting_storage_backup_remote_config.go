package runnerruntime

func storageBackupRemotePolicy(c StorageArchiveExecutionConfig) (backupRemoteNetworkPolicy, error) {
	return databaseBackupRemotePolicy(DatabaseExecutionConfig{RemoteBackups: c.RemoteBackups, RemoteEndpoints: c.RemoteEndpoints, RemoteCIDRs: c.RemoteCIDRs, RemoteFixtureOrigin: c.RemoteFixtureOrigin})
}
