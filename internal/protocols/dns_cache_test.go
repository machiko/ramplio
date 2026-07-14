package protocols

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTCPListener 開一個本機 listener,回傳 host、port 供撥號測試。
func newTCPListener(t *testing.T) (host, port string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	host, port, err = net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)
	return host, port
}

func TestNewDNSCacheDialerDefaultTTL(t *testing.T) {
	assert.Equal(t, 60*time.Second, newDNSCacheDialer(0).ttl, "ttl<=0 應套用 60s 預設")
	assert.Equal(t, 60*time.Second, newDNSCacheDialer(-time.Second).ttl)
	assert.Equal(t, 5*time.Second, newDNSCacheDialer(5*time.Second).ttl, "正值應保留")
}

func TestDNSCacheDialerIPLiteralBypassesCache(t *testing.T) {
	host, port := newTCPListener(t)
	d := newDNSCacheDialer(time.Minute)

	conn, err := d.DialContext(context.Background(), "tcp", net.JoinHostPort(host, port))
	require.NoError(t, err)
	_ = conn.Close()

	assert.Empty(t, d.cache, "IP literal 不應進快取")
}

func TestDNSCacheDialerMalformedAddrFallsBack(t *testing.T) {
	d := newDNSCacheDialer(time.Minute)

	// 無 port 的 addr:SplitHostPort 失敗 → fallback 直撥(由底層 dialer 報錯)。
	_, err := d.DialContext(context.Background(), "tcp", "127.0.0.1")
	require.Error(t, err)
	assert.Empty(t, d.cache, "fallback 路徑不應進快取")
}

func TestDNSCacheDialerUsesCachedEntry(t *testing.T) {
	host, port := newTCPListener(t)
	d := newDNSCacheDialer(time.Minute)

	// 塞一筆假快取:此網域真實解析必失敗,撥號成功即證明走了快取。
	d.cache["fake.invalid"] = dnsCacheEntry{
		addrs:  []string{host},
		expiry: time.Now().Add(time.Minute),
	}

	conn, err := d.DialContext(context.Background(), "tcp", net.JoinHostPort("fake.invalid", port))
	require.NoError(t, err, "快取命中應直接以快取位址撥號")
	_ = conn.Close()
}

func TestDNSCacheDialerExpiredEntryTriggersLookup(t *testing.T) {
	host, port := newTCPListener(t)
	d := newDNSCacheDialer(time.Minute)

	// 塞一筆已過期的假快取:過期即失效,必須重新解析,而 .invalid 保證解析失敗。
	d.cache["fake.invalid"] = dnsCacheEntry{
		addrs:  []string{host},
		expiry: time.Now().Add(-time.Second),
	}

	_, err := d.DialContext(context.Background(), "tcp", net.JoinHostPort("fake.invalid", port))
	require.Error(t, err, "過期條目不可再用,真實解析 .invalid 必失敗")
	assert.Contains(t, err.Error(), "DNS lookup", "錯誤應來自重新解析")
}

func TestDNSCacheDialerResolveCachesLocalhost(t *testing.T) {
	d := newDNSCacheDialer(time.Minute)
	ctx := context.Background()

	addrs, err := d.resolve(ctx, "localhost")
	require.NoError(t, err)
	require.NotEmpty(t, addrs)

	entry, ok := d.cache["localhost"]
	require.True(t, ok, "解析結果應寫入快取")
	assert.Equal(t, addrs, entry.addrs)
	assert.Contains(t, d.order, "localhost", "首次寫入應記錄 FIFO 順序")

	// 竄改快取後再解析:TTL 內應回快取值而非重新查詢。
	d.cache["localhost"] = dnsCacheEntry{addrs: []string{"192.0.2.1"}, expiry: time.Now().Add(time.Minute)}
	cached, err := d.resolve(ctx, "localhost")
	require.NoError(t, err)
	assert.Equal(t, []string{"192.0.2.1"}, cached, "TTL 內應命中快取")
}

func TestDNSCacheDialerFIFOEviction(t *testing.T) {
	d := newDNSCacheDialer(time.Minute)
	d.maxEntries = 2

	seed := func(host string) {
		d.mu.Lock()
		if _, exists := d.cache[host]; !exists {
			if len(d.cache) >= d.maxEntries && len(d.order) > 0 {
				oldest := d.order[0]
				d.order = d.order[1:]
				delete(d.cache, oldest)
			}
			d.order = append(d.order, host)
		}
		d.cache[host] = dnsCacheEntry{addrs: []string{"127.0.0.1"}, expiry: time.Now().Add(time.Minute)}
		d.mu.Unlock()
	}
	// 先塞滿兩筆,再走真實 resolve 寫入第三筆,驗證最舊的被淘汰。
	seed("a.example")
	seed("b.example")

	_, err := d.resolve(context.Background(), "localhost")
	require.NoError(t, err)

	assert.NotContains(t, d.cache, "a.example", "超出上限應淘汰最舊條目")
	assert.Contains(t, d.cache, "b.example")
	assert.Contains(t, d.cache, "localhost")
	assert.Len(t, d.cache, 2)
}

func TestDNSCacheDialerAllAddrsFailReturnsLastErr(t *testing.T) {
	d := newDNSCacheDialer(time.Minute)

	// 快取一個保證撥不通的位址(TEST-NET-1 保留段):所有位址失敗應回最後錯誤。
	d.cache["fake.invalid"] = dnsCacheEntry{
		addrs:  []string{"192.0.2.1"},
		expiry: time.Now().Add(time.Minute),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := d.DialContext(ctx, "tcp", net.JoinHostPort("fake.invalid", "80"))
	require.Error(t, err, "所有已解析位址皆撥號失敗應回傳錯誤")
}
