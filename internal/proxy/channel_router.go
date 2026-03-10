package proxy

import (
	"math/rand"
	"path/filepath"

	"github.com/modelserver/modelserver/internal/types"
)

// ChannelRouter matches requests to channels using route rules.
type ChannelRouter struct {
	channels   []types.Channel
	channelMap map[string]*types.Channel
	routes     []types.ChannelRoute
}

// NewChannelRouter creates a channel router with the given channels and routes.
func NewChannelRouter(channels []types.Channel, routes []types.ChannelRoute) *ChannelRouter {
	cm := make(map[string]*types.Channel, len(channels))
	for i := range channels {
		cm[channels[i].ID] = &channels[i]
	}
	return &ChannelRouter{
		channels:   channels,
		channelMap: cm,
		routes:     routes,
	}
}

// MatchChannels returns the channels to use for a given project + model.
func (cr *ChannelRouter) MatchChannels(projectID, model string) []types.Channel {
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
