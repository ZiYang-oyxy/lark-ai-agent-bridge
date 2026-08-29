package feishu

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

const (
	chatMemberNameSuccessTTL = time.Hour
	chatMemberNameFailureTTL = 30 * time.Second
	chatMemberNameCacheLimit = 1024
)

type ChatMembersAPI interface {
	Get(context.Context, *larkim.GetChatMembersReq, ...larkcore.RequestOptionFunc) (*larkim.GetChatMembersResp, error)
}

type chatMemberNameCacheEntry struct {
	name      string
	err       error
	expiresAt time.Time
}

// ChatMemberNameResolver resolves a sender open_id in the chat where the
// quoted message originated. This uses the same IM visibility as the bot and
// does not require a machine-local lark-cli profile.
type ChatMemberNameResolver struct {
	api        ChatMembersAPI
	mu         sync.Mutex
	cache      map[string]chatMemberNameCacheEntry
	now        func() time.Time
	cacheLimit int
}

func NewChatMemberNameResolver(appID, appSecret string) *ChatMemberNameResolver {
	client := lark.NewClient(appID, appSecret)
	return &ChatMemberNameResolver{
		api:        client.Im.V1.ChatMembers,
		cache:      make(map[string]chatMemberNameCacheEntry),
		now:        time.Now,
		cacheLimit: chatMemberNameCacheLimit,
	}
}

func (r *ChatMemberNameResolver) ResolveChatMemberName(ctx context.Context, chatID, openID string) (string, error) {
	chatID = strings.TrimSpace(chatID)
	openID = strings.TrimSpace(openID)
	if chatID == "" {
		return "", fmt.Errorf("resolve quoted sender name: missing chat id")
	}
	if openID == "" {
		return "", fmt.Errorf("resolve quoted sender name: missing open id")
	}

	now := time.Now()
	if r != nil && r.now != nil {
		now = r.now()
	}
	key := chatID + "\x00" + openID
	if entry, ok := r.cached(key, now); ok {
		return entry.name, entry.err
	}

	name, err := r.fetch(ctx, chatID, openID)
	ttl := chatMemberNameSuccessTTL
	if err != nil {
		ttl = chatMemberNameFailureTTL
	}
	r.store(key, chatMemberNameCacheEntry{name: name, err: err, expiresAt: now.Add(ttl)}, now)
	return name, err
}

func (r *ChatMemberNameResolver) fetch(ctx context.Context, chatID, openID string) (string, error) {
	if r == nil || r.api == nil {
		return "", fmt.Errorf("resolve quoted sender name: chat members API unavailable")
	}
	pageToken := ""
	seenTokens := make(map[string]struct{})
	for {
		builder := larkim.NewGetChatMembersReqBuilder().
			ChatId(chatID).
			MemberIdType("open_id").
			PageSize(100)
		if pageToken != "" {
			builder.PageToken(pageToken)
		}
		resp, err := r.api.Get(ctx, builder.Build())
		if err != nil {
			return "", fmt.Errorf("resolve quoted sender name from chat members: %w", err)
		}
		if resp == nil {
			return "", fmt.Errorf("resolve quoted sender name from chat members: empty response")
		}
		if !resp.Success() {
			return "", fmt.Errorf("resolve quoted sender name from chat members: code=%d msg=%s", resp.Code, resp.Msg)
		}
		if resp.Data == nil {
			return "", fmt.Errorf("resolve quoted sender name from chat members: empty data")
		}
		for _, member := range resp.Data.Items {
			if member == nil || member.MemberId == nil || strings.TrimSpace(*member.MemberId) != openID || member.Name == nil {
				continue
			}
			if name := strings.TrimSpace(*member.Name); name != "" {
				return name, nil
			}
		}
		if resp.Data.HasMore == nil || !*resp.Data.HasMore || resp.Data.PageToken == nil {
			return "", fmt.Errorf("resolve quoted sender name from chat members: member %s not found", openID)
		}
		next := strings.TrimSpace(*resp.Data.PageToken)
		if next == "" {
			return "", fmt.Errorf("resolve quoted sender name from chat members: missing next page token")
		}
		if _, repeated := seenTokens[next]; repeated {
			return "", fmt.Errorf("resolve quoted sender name from chat members: repeated page token")
		}
		seenTokens[next] = struct{}{}
		pageToken = next
	}
}

func (r *ChatMemberNameResolver) cached(key string, now time.Time) (chatMemberNameCacheEntry, bool) {
	if r == nil {
		return chatMemberNameCacheEntry{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.cache[key]
	if !ok || !now.Before(entry.expiresAt) {
		delete(r.cache, key)
		return chatMemberNameCacheEntry{}, false
	}
	return entry, true
}

func (r *ChatMemberNameResolver) store(key string, entry chatMemberNameCacheEntry, now time.Time) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cache == nil {
		r.cache = make(map[string]chatMemberNameCacheEntry)
	}
	for cachedKey, cached := range r.cache {
		if !now.Before(cached.expiresAt) {
			delete(r.cache, cachedKey)
		}
	}
	limit := r.cacheLimit
	if limit <= 0 {
		limit = chatMemberNameCacheLimit
	}
	if _, exists := r.cache[key]; !exists && len(r.cache) >= limit {
		var evictKey string
		var earliest time.Time
		for cachedKey, cached := range r.cache {
			if evictKey == "" || cached.expiresAt.Before(earliest) {
				evictKey = cachedKey
				earliest = cached.expiresAt
			}
		}
		delete(r.cache, evictKey)
	}
	r.cache[key] = entry
}
