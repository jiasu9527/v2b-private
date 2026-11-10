package newapibridge

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxCharacterImportBytes = 2 << 20
const maxCharacterJSONBytes = 256 << 10

var ErrUnsupportedCharacter = errors.New("仅支持 Character Card V2；V3 和脚本扩展尚不支持")

type CharacterCard struct {
	Spec        string        `json:"spec"`
	SpecVersion string        `json:"spec_version"`
	Data        CharacterData `json:"data"`
}

type CharacterData struct {
	Name                    string                     `json:"name"`
	Description             string                     `json:"description"`
	Personality             string                     `json:"personality"`
	Scenario                string                     `json:"scenario"`
	FirstMessage            string                     `json:"first_mes"`
	MessageExample          string                     `json:"mes_example"`
	CreatorNotes            string                     `json:"creator_notes"`
	SystemPrompt            string                     `json:"system_prompt"`
	PostHistoryInstructions string                     `json:"post_history_instructions"`
	AlternateGreetings      []string                   `json:"alternate_greetings"`
	CharacterBook           *LoreBook                  `json:"character_book,omitempty"`
	Tags                    []string                   `json:"tags"`
	Creator                 string                     `json:"creator"`
	CharacterVersion        string                     `json:"character_version"`
	Extensions              map[string]json.RawMessage `json:"extensions"`
}

type Character struct {
	ID        int64         `json:"id"`
	Version   int64         `json:"version"`
	Data      CharacterData `json:"data"`
	CreatedAt int64         `json:"created_at"`
	UpdatedAt int64         `json:"updated_at"`
}
type CharacterSaveRequest struct {
	ID              int64         `json:"id"`
	ExpectedVersion int64         `json:"expected_version"`
	Data            CharacterData `json:"data"`
}
type CharacterPage struct {
	Items    []Character `json:"items"`
	Total    int64       `json:"total"`
	Current  int         `json:"current"`
	PageSize int         `json:"page_size"`
}

func normalizeCharacter(data *CharacterData) error {
	data.Name = strings.TrimSpace(data.Name)
	if data.Name == "" || utf8.RuneCountInString(data.Name) > 100 || len(data.Tags) > 50 || len(data.AlternateGreetings) > 20 {
		return ErrInvalid
	}
	if data.Extensions == nil {
		data.Extensions = map[string]json.RawMessage{}
	}
	if data.Tags == nil {
		data.Tags = []string{}
	}
	if data.AlternateGreetings == nil {
		data.AlternateGreetings = []string{}
	}
	for _, v := range []string{data.Description, data.Personality, data.Scenario, data.FirstMessage, data.MessageExample, data.CreatorNotes, data.SystemPrompt, data.PostHistoryInstructions} {
		if !utf8.ValidString(v) || len(v) > 32768 || strings.ContainsRune(v, 0) {
			return ErrInvalid
		}
	}
	for _, v := range data.AlternateGreetings {
		if len(v) > 32768 || !utf8.ValidString(v) {
			return ErrInvalid
		}
	}
	for _, v := range data.Tags {
		if len(v) > 100 {
			return ErrInvalid
		}
	}
	if len(data.Creator) > 200 || len(data.CharacterVersion) > 100 {
		return ErrInvalid
	}
	if data.CharacterBook != nil {
		if err := normalizeLoreBook(data.CharacterBook); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(data)
	if err != nil || len(raw) > maxCharacterJSONBytes {
		return ErrInvalid
	}
	return nil
}

func (s *Service) SaveCharacter(ctx context.Context, userID int64, req CharacterSaveRequest) (Character, error) {
	if userID <= 0 {
		return Character{}, ErrForbidden
	}
	if req.ID < 0 || req.ExpectedVersion < 0 || (req.ID == 0 && req.ExpectedVersion != 0) || (req.ID > 0 && req.ExpectedVersion < 1) {
		return Character{}, ErrInvalid
	}
	if err := normalizeCharacter(&req.Data); err != nil {
		return Character{}, err
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return Character{}, err
	}
	raw, _ := json.Marshal(req.Data)
	now := time.Now().Unix()
	c := Character{Data: req.Data, CreatedAt: now, UpdatedAt: now}
	var err error
	if req.ID == 0 {
		err = s.db.QueryRowContext(ctx, `INSERT INTO v2_newapi_character(user_id,version,data,created_at,updated_at) VALUES($1,1,$2,$3,$3) RETURNING id,version`, userID, string(raw), now).Scan(&c.ID, &c.Version)
	} else {
		err = s.db.QueryRowContext(ctx, `UPDATE v2_newapi_character SET data=$1,version=version+1,updated_at=$2 WHERE id=$3 AND user_id=$4 AND version=$5 RETURNING id,version,created_at`, string(raw), now, req.ID, userID, req.ExpectedVersion).Scan(&c.ID, &c.Version, &c.CreatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return Character{}, ErrConflict
		}
	}
	return c, err
}

func scanCharacter(row interface{ Scan(...any) error }) (Character, error) {
	var c Character
	var raw []byte
	if err := row.Scan(&c.ID, &c.Version, &raw, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return c, err
	}
	if err := json.Unmarshal(raw, &c.Data); err != nil {
		return c, ErrUnavailable
	}
	return c, nil
}
func (s *Service) GetCharacter(ctx context.Context, userID, id int64) (Character, error) {
	if userID <= 0 || id <= 0 {
		return Character{}, ErrForbidden
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return Character{}, err
	}
	c, err := scanCharacter(s.db.QueryRowContext(ctx, `SELECT id,version,data,created_at,updated_at FROM v2_newapi_character WHERE user_id=$1 AND id=$2`, userID, id))
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrForbidden
	}
	return c, err
}
func (s *Service) ListCharacters(ctx context.Context, userID int64, current, size int) (CharacterPage, error) {
	p := CharacterPage{Items: []Character{}, Current: current, PageSize: size}
	if userID <= 0 {
		return p, ErrForbidden
	}
	if current < 1 || current > 100000 || size < 1 || size > 100 {
		return p, ErrInvalid
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return p, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_newapi_character WHERE user_id=$1`, userID).Scan(&p.Total); err != nil {
		return p, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,version,data,created_at,updated_at FROM v2_newapi_character WHERE user_id=$1 ORDER BY updated_at DESC,id DESC LIMIT $2 OFFSET $3`, userID, size, (current-1)*size)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		c, e := scanCharacter(rows)
		if e != nil {
			return p, e
		}
		p.Items = append(p.Items, c)
	}
	return p, rows.Err()
}
func (s *Service) DeleteCharacter(ctx context.Context, userID, id int64) error {
	if userID <= 0 || id <= 0 {
		return ErrForbidden
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM v2_newapi_character WHERE user_id=$1 AND id=$2`, userID, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return ErrForbidden
	}
	return nil
}
func (s *Service) ExportCharacter(ctx context.Context, userID, id int64) (CharacterCard, error) {
	c, err := s.GetCharacter(ctx, userID, id)
	return CharacterCard{Spec: "chara_card_v2", SpecVersion: "2.0", Data: c.Data}, err
}
func (s *Service) ImportCharacter(ctx context.Context, userID int64, raw []byte) (Character, error) {
	card, err := ParseCharacterCard(raw)
	if err != nil {
		return Character{}, err
	}
	return s.SaveCharacter(ctx, userID, CharacterSaveRequest{Data: card.Data})
}

// ParseCharacterCard only extracts structured data; embedded extensions are
// preserved for export and are never interpreted as JavaScript or tool calls.
func ParseCharacterCard(raw []byte) (CharacterCard, error) {
	var card CharacterCard
	if len(raw) == 0 || len(raw) > MaxCharacterImportBytes {
		return card, ErrInvalid
	}
	if bytes.HasPrefix(raw, []byte("\x89PNG\r\n\x1a\n")) {
		var err error
		raw, err = characterPNGData(raw)
		if err != nil {
			return card, err
		}
	}
	if len(raw) > maxCharacterJSONBytes || !utf8.Valid(raw) {
		return card, ErrInvalid
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&card); err != nil {
		return card, ErrInvalid
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return card, ErrInvalid
	}
	if card.Spec != "chara_card_v2" || card.SpecVersion != "2.0" {
		return card, ErrUnsupportedCharacter
	}
	if err := normalizeCharacter(&card.Data); err != nil {
		return card, err
	}
	return card, nil
}

func characterPNGData(raw []byte) ([]byte, error) {
	var card []byte
	hasIEND := false
	for offset := 8; offset < len(raw); {
		if len(raw)-offset < 12 {
			return nil, ErrInvalid
		}
		n := int(binary.BigEndian.Uint32(raw[offset : offset+4]))
		if n > len(raw)-offset-12 {
			return nil, ErrInvalid
		}
		kind := string(raw[offset+4 : offset+8])
		data := raw[offset+8 : offset+8+n]
		if crc32.ChecksumIEEE(raw[offset+4:offset+8+n]) != binary.BigEndian.Uint32(raw[offset+8+n:offset+12+n]) {
			return nil, ErrInvalid
		}
		if offset == 8 {
			if kind != "IHDR" || n != 13 {
				return nil, ErrInvalid
			}
			w, h := binary.BigEndian.Uint32(data[:4]), binary.BigEndian.Uint32(data[4:8])
			if w == 0 || h == 0 || w > 4096 || h > 4096 || uint64(w)*uint64(h) > 16000000 {
				return nil, ErrInvalid
			}
		}
		if kind == "tEXt" {
			zero := bytes.IndexByte(data, 0)
			if zero >= 0 {
				name := strings.ToLower(string(data[:zero]))
				if name == "ccv3" {
					return nil, ErrUnsupportedCharacter
				}
				if name == "chara" {
					if card != nil || len(data) > maxCharacterJSONBytes*2 {
						return nil, ErrInvalid
					}
					decoded, err := base64.StdEncoding.DecodeString(string(data[zero+1:]))
					if err != nil {
						return nil, ErrInvalid
					}
					card = decoded
				}
			}
		}
		offset += 12 + n
		if kind == "IEND" {
			if n != 0 || offset != len(raw) {
				return nil, ErrInvalid
			}
			hasIEND = true
			break
		}
	}
	if !hasIEND || len(card) == 0 {
		return nil, fmt.Errorf("%w：PNG 中没有 V2 chara 数据", ErrInvalid)
	}
	return card, nil
}
