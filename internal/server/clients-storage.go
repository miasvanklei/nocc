package server

import (
	"fmt"
	"os"
	"path"
	"sync"
	"time"
)

// defaultMappedFolders are folders that are bind-mounted to a client working directory.
// They are read-only, so a client can't modify them.
// We assume that /bin and /lib are symlinked to /usr/bin and /usr/lib, respectively
var defaultMappedFolders = []string{
	"/lib",
	"/bin",
	"/etc",
}

// ClientsStorage contains all active clients connected to this server.
// After a client is not active for some time, it's deleted (and its working directory is removed from a hard disk).
type ClientsStorage struct {
	table map[string]*Client
	mu    sync.RWMutex

	romountDirs RoMountPaths
	rwmountDirs RwMountPaths
	clientsDir  string // /tmp/nocc/cpp/clients

	lastPurgeTime time.Time

	uniqueRemotesList map[string]string
}

func MakeClientsStorage(clientsDir string, compilerDirs []string, objcacheDir string) (*ClientsStorage, error) {
	return &ClientsStorage{
		table:             make(map[string]*Client, 1024),
		clientsDir:        clientsDir,
		uniqueRemotesList: make(map[string]string, 1),
		romountDirs:       makeRoMountPaths(append(defaultMappedFolders, compilerDirs...)...),
		rwmountDirs:       makeRwMountPaths(objcacheDir),
	}, nil
}

func (allClients *ClientsStorage) GetClient(clientID string) *Client {
	allClients.mu.RLock()
	client := allClients.table[clientID]
	allClients.mu.RUnlock()

	return client
}

func (allClients *ClientsStorage) OnClientConnected(clientID string) (*Client, error) {
	allClients.mu.RLock()
	client := allClients.table[clientID]
	allClients.mu.RUnlock()

	// rpc query /StartClient is sent exactly once by nocc-daemon
	// if this clientID exists in table, this means a previous interrupted nocc-daemon launch
	// in this case, delete an old hanging client, closing all channels and streams — and create a new instance
	if client != nil {
		logServer.Info(0, "client reconnected, re-creating", "clientID", clientID)
		allClients.DeleteClient(client)
	}

	workingDir := path.Join(allClients.clientsDir, clientID)
	if err := os.Mkdir(workingDir, os.ModePerm); err != nil {
		return nil, fmt.Errorf("can't create client working directory: %v", err)
	}

	if err := BindmountPaths(workingDir, allClients.romountDirs.MountPaths); err != nil {
		return nil, err
	}
	if err := BindmountPaths(workingDir, allClients.rwmountDirs.MountPaths); err != nil {
		return nil, err
	}

	client = &Client{
		clientID:          clientID,
		workingDir:        workingDir,
		lastSeen:          time.Now(),
		sessions:          make(map[uint32]*Session, 20),
		files:             make(map[string]*fileInClientDir, 1024),
		dirs:              make(map[string]bool, 100),
		chanDisconnected:  make(chan struct{}),
		chanReadySessions: make(chan *Session, 200),
	}

	allClients.mu.Lock()
	allClients.table[clientID] = client
	allClients.mu.Unlock()
	return client, nil
}

func (allClients *ClientsStorage) DeleteClient(client *Client) {
	allClients.mu.Lock()
	delete(allClients.table, client.clientID)
	allClients.mu.Unlock()

	workingDir := path.Join(allClients.clientsDir, client.clientID)
	UnmountPaths(workingDir, allClients.romountDirs.MountPaths)
	UnmountPaths(workingDir, allClients.rwmountDirs.MountPaths)

	close(client.chanDisconnected)
	// don't close chanReadySessions intentionally, it's not a leak
	client.RemoveWorkingDir()
}

func (allClients *ClientsStorage) DeleteInactiveClients() {
	now := time.Now()
	if now.Sub(allClients.lastPurgeTime) < time.Minute {
		return
	}

	for {
		var inactiveClient *Client = nil
		allClients.mu.RLock()
		for _, client := range allClients.table {
			if now.Sub(client.lastSeen) > 5*time.Minute {
				inactiveClient = client
				break
			}
		}
		allClients.mu.RUnlock()
		if inactiveClient == nil {
			break
		}

		logServer.Info(0, "delete inactive client", "clientID", inactiveClient.clientID, "num files", inactiveClient.FilesCount(), "; nClients", allClients.ActiveCount()-1)
		allClients.DeleteClient(inactiveClient)
	}
}

func (allClients *ClientsStorage) StopAllClients() {
	allClients.mu.Lock()
	for _, client := range allClients.table {
		// do not call DeleteClient(), since the server is stopping, removing working dir is not needed
		close(client.chanDisconnected)
	}

	allClients.table = make(map[string]*Client)
	allClients.mu.Unlock()
}

func (allClients *ClientsStorage) ActiveCount() int64 {
	allClients.mu.RLock()
	clientsCount := len(allClients.table)
	allClients.mu.RUnlock()
	return int64(clientsCount)
}

func (allClients *ClientsStorage) ActiveSessionsCount() int64 {
	allClients.mu.RLock()
	sessionsCount := 0
	for _, client := range allClients.table {
		sessionsCount += client.GetActiveSessionsCount()
	}
	allClients.mu.RUnlock()
	return int64(sessionsCount)
}

func (allClients *ClientsStorage) TotalFilesCountInDirs() int64 {
	var filesCount int64 = 0
	allClients.mu.RLock()
	for _, client := range allClients.table {
		filesCount += client.FilesCount()
	}
	allClients.mu.RUnlock()
	return filesCount
}
