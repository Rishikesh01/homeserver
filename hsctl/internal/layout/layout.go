// Package layout is the stack's shape: which compose projects (folders) make it up and in what
// order they start, which containers and volumes belong to it, and the network they share.
package layout

// Services are the compose project folders in start order. Caddy is last so it can proxy the rest;
// stopping reverses the order.
var Services = []string{"vaultwarden", "nextcloud", "pihole", "stirling", "it-tools", "imagetools", "caddy"}

// Proxy is the reverse proxy's folder and container: the HTTPS front door, never an app.
const Proxy = "caddy"

// Core are the apps that need a generated .env (the tools run from compose defaults).
var Core = []string{"vaultwarden", "nextcloud", "pihole"}

// ContainerFilters match the stack's containers by name (docker matches substrings, so
// "nextcloud" covers nextcloud-app/-db/-redis).
var ContainerFilters = []string{"vaultwarden", "nextcloud", "pihole", "caddy", "stirling-pdf", "it-tools", "imagetools"}

// Containers are every compose file's fixed container_name.
var Containers = []string{"caddy", "vaultwarden", "nextcloud-app", "nextcloud-db", "nextcloud-redis", "pihole", "stirling-pdf", "it-tools", "imagetools"}

// ComposeProjects are the volume-name prefixes compose has ever used for the stack
// ("<project>_<volume>", project = folder name).
var ComposeProjects = []string{"caddy", "vaultwarden", "nextcloud", "pihole", "stirling", "it-tools", "imagetools"}

// EdgeNetwork is the shared reverse-proxy network Caddy and every app attach to (declared
// `external` in the compose files). It replaces host-published app ports: Caddy reaches each app
// by its container name over this network, so the apps expose no plaintext HTTP on the LAN.
const EdgeNetwork = "homeserver-edge"

// The apps whose data needs special care in a backup.
const (
	Vaultwarden       = "vaultwarden"         // container
	VaultwardenVolume = "vaultwarden_vw-data" // its data volume
	NextcloudApp      = "nextcloud-app"
	NextcloudDB       = "nextcloud-db" // the Postgres container
	NextcloudFolder   = "nextcloud"    // its compose project (holds nextcloud/.env)
	NextcloudDBSvc    = "db"           // the database's compose service
)

// NextcloudDBVolumes are the names Nextcloud's Postgres data volume has had, newest first: the
// compose file moved to a fresh volume for the Postgres 18 data layout.
var NextcloudDBVolumes = []string{"nextcloud_db-data-v18", "nextcloud_db-data"}

// CacheVolumes are volumes a backup skips: pure caches the service rebuilds on its own.
var CacheVolumes = map[string]bool{
	"nextcloud_redis-data": true, // Nextcloud's Redis cache + file locks; rebuilt on start
}

// CaddyCA is where Caddy keeps the root certificate of its local CA, inside its container.
const CaddyCA = "/data/caddy/pki/authorities/local/root.crt"

// Apps are the switchable compose projects, in start order: everything but the proxy.
func Apps() []string {
	var out []string
	for _, s := range Services {
		if s != Proxy {
			out = append(out, s)
		}
	}
	return out
}

// IsApp reports whether dir is one of the switchable apps.
func IsApp(dir string) bool {
	for _, d := range Apps() {
		if d == dir {
			return true
		}
	}
	return false
}

// FolderFor maps a container name to its compose folder (nextcloud-db -> nextcloud,
// stirling-pdf -> stirling; the rest match).
func FolderFor(container string) string {
	switch {
	case len(container) >= 9 && container[:9] == "nextcloud":
		return "nextcloud"
	case container == "stirling-pdf":
		return "stirling"
	}
	return container
}
