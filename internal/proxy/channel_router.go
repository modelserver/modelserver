package proxy

import (
	"log/slog"
	"math/rand"
	"path/filepath"
	"sync"

	"github.com/modelserver/modelserver/internal/crypto"
	"github.com/modelserver/modelserver/internal/types"
)

// ChannelRouter matches requests to channels using route rules.
// Decrypted channel API keys are cached in memory to avoid per-request decryption.
type ChannelRouter struct {
	mu            sync.RWMutex
	channels      []types.Channel
	channelMap    map[string]*types.Channel
	routes        []types.ChannelRoute
	decryptedKeys map[string]string // channelID → plaintext API key
}

// NewChannelRouter creates a channel router with the given channels and routes.
// Decrypts all channel API keys at construction time.
func NewChannelRouter(channels []types.Channel, routes []types.ChannelRoute, encKey []byte, logger *slog.Logger) *ChannelRouter {
	cm := make(map[string]*types.Channel, len(channels))
	for i := range channels {
		cm[channels[i].ID] = &channels[i]
	}
	keys := decryptChannelKeys(channels, encKey, logger)
	return &ChannelRouter{
		channels:      channels,
		channelMap:    cm,
		routes:        routes,
		decryptedKeys: keys,
	}
}

// Reload replaces the channels and routes atomically, re-decrypting all keys.
func (cr *ChannelRouter) Reload(channels []types.Channel, routes []types.ChannelRoute, encKey []byte, logger *slog.Logger) {
	cm := make(map[string]*types.Channel, len(channels))
	for i := range channels {
		cm[channels[i].ID] = &channels[i]
	}
	keys := decryptChannelKeys(channels, encKey, logger)
	cr.mu.Lock()
	cr.channels = channels
	cr.channelMap = cm
	cr.routes = routes
	cr.decryptedKeys = keys
	cr.mu.Unlock()
}

// GetChannelKey returns the decrypted API key for a channel, or empty string if not found.
func (cr *ChannelRouter) GetChannelKey(channelID string) string {
	cr.mu.RLock()
	defer cr.mu.RUnlock()
	return cr.decryptedKeys[channelID]
}

// MatchChannels returns the channels to use for a given project + model.
func (cr *ChannelRouter) MatchChannels(projectID, model string) []types.Channel {
	cr.mu.RLock()
	defer cr.mu.RUnlock()

	if ch := cr.matchRoutes(projectID, model); len(ch) > 0 {
		return ch
	}
	if ch := cr.matchRoutes("", model); len(ch) > 0 {
		return ch
	}

	var result []types.Channel
	for _, c := range cr.channels {
		if c.Status == types.ChannelStatusActive && modelSupported(c.SupportedModels, model) {
			result = append(result, c)
		}
	}
	return result
}

// ActiveModels returns all supported models from active channels.
func (cr *ChannelRouter) ActiveModels() []string {
	cr.mu.RLock()
	defer cr.mu.RUnlock()

	seen := make(map[string]bool)
	var models []string
	for _, ch := range cr.channels {
		if ch.Status == types.ChannelStatusActive {
			for _, m := range ch.SupportedModels {
				if !seen[m] {
					seen[m] = true
					models = append(models, m)
				}
			}
		}
	}
	return models
}

func (cr *ChannelRouter) matchRoutes(projectID, model string) []types.Channel {
	for _, route := range cr.routes {
		if !route.Enabled {
			continue
		}
		if route.ProjectID != projectID {
			continue
		}
		if !matchModel(route.ModelPattern, model) {
			continue
		}

		var channels []types.Channel
		for _, id := range route.ChannelIDs {
			if ch, ok := cr.channelMap[id]; ok && ch.Status == types.ChannelStatusActive {
				channels = append(channels, *ch)
			}
		}
		if len(channels) > 0 {
			return channels
		}
	}
	return nil
}

func matchModel(pattern, model string) bool {
	if pattern == "*" {
		return true
	}
	matched, _ := filepath.Match(pattern, model)
	return matched
}

func modelSupported(supported []string, model string) bool {
	for _, s := range supported {
		if s == model {
			return true
		}
	}
	return false
}

// SelectChannel picks a channel from the list using priority grouping and weighted random.
func SelectChannel(channels []types.Channel) *types.Channel {
	if len(channels) == 0 {
		return nil
	}

	groups := make(map[int][]types.Channel)
	maxPriority := channels[0].SelectionPriority
	for _, c := range channels {
		groups[c.SelectionPriority] = append(groups[c.SelectionPriority], c)
		if c.SelectionPriority > maxPriority {
			maxPriority = c.SelectionPriority
		}
	}

	for p := maxPriority; p >= 0; p-- {
		group, ok := groups[p]
		if !ok || len(group) == 0 {
			continue
		}
		return weightedRandom(group)
	}

	return &channels[0]
}

func weightedRandom(channels []types.Channel) *types.Channel {
	totalWeight := 0
	for _, c := range channels {
		w := c.Weight
		if w <= 0 {
			w = 1
		}
		totalWeight += w
	}

	r := rand.Intn(totalWeight)
	for i := range channels {
		w := channels[i].Weight
		if w <= 0 {
			w = 1
		}
		r -= w
		if r < 0 {
			return &channels[i]
		}
	}
	return &channels[0]
}

// decryptChannelKeys decrypts all channel API keys and returns a map of channelID → plaintext.
func decryptChannelKeys(channels []types.Channel, encKey []byte, logger *slog.Logger) map[string]string {
	keys := make(map[string]string, len(channels))
	if len(encKey) == 0 {
		return keys
	}
	for _, ch := range channels {
		if len(ch.APIKeyEncrypted) == 0 {
			continue
		}
		plaintext, err := crypto.Decrypt(encKey, ch.APIKeyEncrypted)
		if err != nil {
			if logger != nil {
				logger.Error("failed to decrypt channel key at load time", "channel_id", ch.ID, "error", err)
			}
			continue
		}
		keys[ch.ID] = string(plaintext)
	}
	return keys
}
