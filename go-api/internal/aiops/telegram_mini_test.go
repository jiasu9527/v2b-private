package aiops

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func miniFixtureInitData(bot string, id int64, stamp int64, extra url.Values) string {
	user, _ := json.Marshal(map[string]any{"id": id, "first_name": "Fixture", "is_bot": false})
	values := url.Values{"auth_date": {strconv.FormatInt(stamp, 10)}, "query_id": {"fixture-query"}, "user": {string(user)}}
	for key, valuesForKey := range extra {
		values[key] = valuesForKey
	}
	return miniFixtureSign(bot, values)
}

func miniFixtureSign(bot string, values url.Values) string {
	var lines []string
	for key, value := range values {
		if key != "hash" {
			lines = append(lines, key+"="+value[0])
		}
	}
	sort.Strings(lines)
	key := hmac.New(sha256.New, []byte("WebAppData"))
	_, _ = key.Write([]byte(bot))
	signature := hmac.New(sha256.New, key.Sum(nil))
	_, _ = signature.Write([]byte(strings.Join(lines, "\n")))
	values.Set("hash", hex.EncodeToString(signature.Sum(nil)))
	return values.Encode()
}

func TestTelegramMiniInitDataVerification(t *testing.T) {
	now := time.Unix(1793500000, 0)
	bot := "123456:fixture_bot_token"
	// Independently generated with Python's hmac/hashlib and kept fixed, so a
	// matching defect in the fixture signer cannot silently change the algorithm.
	vector := "auth_date=1793500000&query_id=fixture-query&user=%7B%22first_name%22%3A%22Fixture%22%2C%22id%22%3A98765%2C%22is_bot%22%3Afalse%7D&hash=fb75c2f35def999333aaf41fee1d05298ea0cd6399ea29fa35a774a05dce1338"
	if id, err := verifyMiniInitData(vector, bot, now); err != nil || id != 98765 {
		t.Fatal("fixed HMAC vector", id, err)
	}
	good := miniFixtureInitData(bot, 98765, now.Unix(), nil)
	if id, err := verifyMiniInitData(good, bot, now); err != nil || id != 98765 {
		t.Fatal(id, err)
	}
	for name, raw := range map[string]string{
		"expired":        miniFixtureInitData(bot, 98765, now.Unix()-301, nil),
		"future":         miniFixtureInitData(bot, 98765, now.Unix()+31, nil),
		"negative id":    miniFixtureInitData(bot, -1, now.Unix(), nil),
		"zero id":        miniFixtureInitData(bot, 0, now.Unix(), nil),
		"overflow id":    miniFixtureInitData(bot, 1, now.Unix(), url.Values{"user": {`{"id":9223372036854775808}`}}),
		"float id":       miniFixtureInitData(bot, 1, now.Unix(), url.Values{"user": {`{"id":1.5}`}}),
		"bot user":       miniFixtureInitData(bot, 1, now.Unix(), url.Values{"user": {`{"id":1,"is_bot":true}`}}),
		"bad user":       miniFixtureInitData(bot, 1, now.Unix(), url.Values{"user": {`{`}}),
		"missing user":   miniFixtureInitData(bot, 1, now.Unix(), url.Values{"user": {``}}),
		"invalid date":   miniFixtureInitData(bot, 1, now.Unix(), url.Values{"auth_date": {`NaN`}}),
		"other bot":      miniFixtureInitData("123456:wrong_token", 98765, now.Unix(), nil),
		"duplicate":      good + "&auth_date=1793500000",
		"duplicate hash": good + "&hash=0000",
		"bad escape":     good + "&broken=%XY",
		"empty":          "",
		"oversized":      good + "&pad=" + strings.Repeat("x", 16384),
		"unsigned":       "auth_date=1793500000&user=%7B%22id%22%3A1%7D",
		"tamper":         strings.Replace(good, "fixture-query", "changed-query", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := verifyMiniInitData(raw, bot, now); err == nil {
				t.Fatal("invalid data accepted")
			}
		})
	}
	for _, stamp := range []int64{now.Unix() - 300, now.Unix() + 30} {
		if _, err := verifyMiniInitData(miniFixtureInitData(bot, 2, stamp, url.Values{"signature": {"telegram-ed25519-value"}}), bot, now); err != nil {
			t.Fatal("boundary or signature field rejected", err)
		}
	}
}
