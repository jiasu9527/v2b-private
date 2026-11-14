package aiops

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type sshStageRef struct {
	IntakeID  int64  `json:"intake_id"`
	ItemIndex int    `json:"item_index"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Username  string `json:"username"`
}

func validateSSHStageItem(item sshAIExtracted) (SSHRegistrationInput, error) {
	host, endpointPort, err := sshIntakeEndpoint(item.Host, true)
	if err != nil {
		return SSHRegistrationInput{}, ErrValidation
	}
	port := item.Port
	if port == 0 {
		port = endpointPort
	}
	if port == 0 {
		port = 22
	}
	if port < 1 || port > 65535 || endpointPort != 0 && endpointPort != port {
		return SSHRegistrationInput{}, ErrValidation
	}
	username := strings.TrimSpace(item.Username)
	if !sshUserRE.MatchString(username) || strings.ContainsAny(username, "\r\n\x00") || (item.Password == "") == (item.PrivateKey == "") || item.Passphrase != "" && item.PrivateKey == "" || len(item.Password) > 4096 || len(item.PrivateKey) > 32768 || len(item.Passphrase) > 4096 {
		return SSHRegistrationInput{}, ErrValidation
	}
	name := strings.TrimSpace(item.Name)
	if name == "" {
		name = host
	}
	if !utf8.ValidString(name) || strings.ContainsAny(name, "\r\n\x00") || len([]rune(name)) > 128 || !utf8.ValidString(item.Password) || !utf8.ValidString(item.PrivateKey) || !utf8.ValidString(item.Passphrase) || strings.ContainsRune(item.Password, 0) || strings.ContainsRune(item.PrivateKey, 0) || strings.ContainsRune(item.Passphrase, 0) {
		return SSHRegistrationInput{}, ErrValidation
	}
	group, err := normalizeServerGroup(item.Group)
	if err != nil {
		return SSHRegistrationInput{}, ErrValidation
	}
	if item.Fingerprint != "" {
		decoded, decodeErr := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(item.Fingerprint, "SHA256:"))
		if !strings.HasPrefix(item.Fingerprint, "SHA256:") || decodeErr != nil || len(decoded) != 32 {
			return SSHRegistrationInput{}, ErrValidation
		}
	}
	if item.PrivateKey != "" {
		if _, err := parseSSHKey(item.PrivateKey, item.Passphrase); err != nil {
			return SSHRegistrationInput{}, ErrValidation
		}
	}
	return SSHRegistrationInput{Name: name, Group: group, Host: host, Port: port, Username: username, Password: item.Password, PrivateKey: item.PrivateKey, KeyPassphrase: item.Passphrase, HostKeySHA256: item.Fingerprint}, nil
}

// The model translates the operator's arbitrary prose into typed items here.
// This tool only stores encrypted intakes; ssh_server_register remains the
// separate approval/registration boundary. Tool-call arguments are redacted
// before persistence by redactKnowledgeMessages.
func (s *Service) callServerStageTool(ctx context.Context, actorID, sessionID int64, call ToolCall) (ToolOutcome, error) {
	var args struct {
		Items []sshAIExtracted `json:"items"`
	}
	d := json.NewDecoder(strings.NewReader(call.Function.Arguments))
	d.DisallowUnknownFields()
	if call.Function.Name != "ssh_server_stage" || d.Decode(&args) != nil || d.Decode(new(any)) != io.EOF || len(args.Items) == 0 || len(args.Items) > maxSSHIntakeBatchSize {
		return ToolOutcome{}, ErrValidation
	}
	turnID, _ := ctx.Value(knowledgeTurnContextKey{}).(int64)
	if turnID <= 0 || actorID <= 0 || sessionID <= 0 || call.ID == "" || len(call.ID) > 250 {
		return ToolOutcome{}, ErrForbidden
	}
	inputs := make([]SSHRegistrationInput, 0, len(args.Items))
	seen := map[string]bool{}
	for _, item := range args.Items {
		input, err := validateSSHStageItem(item)
		if err != nil {
			return ToolOutcome{}, ErrValidation
		}
		identity := input.Host + "\x00" + input.Username + "\x00" + strconv.Itoa(input.Port)
		if seen[identity] {
			return ToolOutcome{}, ErrValidation
		}
		seen[identity] = true
		inputs = append(inputs, input)
	}
	if err := s.ensureSchema(ctx); err != nil {
		return ToolOutcome{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ToolOutcome{}, err
	}
	defer tx.Rollback()
	if err := lockAIState(ctx, tx); err != nil {
		return ToolOutcome{}, err
	}
	if err := s.requireOperatorTx(ctx, tx, actorID); err != nil {
		return ToolOutcome{}, err
	}
	if err := s.requireSessionAccessTx(ctx, tx, actorID, sessionID); err != nil {
		return ToolOutcome{}, err
	}
	now := time.Now().Unix()
	var status string
	var stop bool
	var expires int64
	if err := tx.QueryRowContext(ctx, `SELECT status,stop_requested,expires_at FROM v2_ai_turn WHERE id=$1 AND session_id=$2 AND actor_id=$3 FOR UPDATE`, turnID, sessionID, actorID).Scan(&status, &stop, &expires); err != nil {
		return ToolOutcome{}, ErrForbidden
	}
	if status != "running" || stop || expires <= now {
		return ToolOutcome{}, ErrConflict
	}
	requestHash := hashOperation(call.Function.Arguments)
	rows, err := tx.QueryContext(ctx, `SELECT id,item_index,host,port,username,tool_request_hash FROM v2_ai_server_intake WHERE turn_id=$1 AND session_id=$2 AND actor_id=$3 AND tool_call_id=$4 ORDER BY item_index`, turnID, sessionID, actorID, call.ID)
	if err != nil {
		return ToolOutcome{}, err
	}
	refs := make([]sshStageRef, 0, len(inputs))
	for rows.Next() {
		var ref sshStageRef
		var oldHash string
		if err := rows.Scan(&ref.IntakeID, &ref.ItemIndex, &ref.Host, &ref.Port, &ref.Username, &oldHash); err != nil {
			rows.Close()
			return ToolOutcome{}, err
		}
		if oldHash != requestHash {
			rows.Close()
			return ToolOutcome{}, ErrConflict
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ToolOutcome{}, err
	}
	rows.Close()
	if len(refs) != 0 {
		if len(refs) != len(inputs) {
			return ToolOutcome{}, ErrConflict
		}
		return sshStageOutcome(turnID, refs, true), nil
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_ai_server_intake WHERE turn_id=$1 AND session_id=$2 AND actor_id=$3`, turnID, sessionID, actorID).Scan(&count); err != nil {
		return ToolOutcome{}, err
	}
	if count+len(inputs) > maxSSHIntakeBatchSize {
		return ToolOutcome{}, fmt.Errorf("%w：本轮最多暂存 50 台服务器，本批未新增资料", ErrValidation)
	}
	for i, input := range inputs {
		secrets := []string{input.Password, input.PrivateKey, input.KeyPassphrase}
		ciphers := make([]string, len(secrets))
		for index, secret := range secrets {
			if secret == "" {
				continue
			}
			ciphers[index], err = s.encryptSecret(secret)
			if err != nil {
				return ToolOutcome{}, err
			}
		}
		ref := sshStageRef{ItemIndex: count + i + 1, Host: input.Host, Port: input.Port, Username: input.Username}
		err = tx.QueryRowContext(ctx, `INSERT INTO v2_ai_server_intake(session_id,turn_id,actor_id,item_index,name,host,port,username,host_key_sha256,password_cipher,private_key_cipher,key_passphrase_cipher,group_name,tool_call_id,tool_request_hash,created_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17) RETURNING id`, sessionID, turnID, actorID, ref.ItemIndex, input.Name, input.Host, input.Port, input.Username, input.HostKeySHA256, ciphers[0], ciphers[1], ciphers[2], input.Group, call.ID, requestHash, now, now+86400).Scan(&ref.IntakeID)
		if err != nil {
			return ToolOutcome{}, err
		}
		refs = append(refs, ref)
	}
	if err := tx.Commit(); err != nil {
		return ToolOutcome{}, err
	}
	return sshStageOutcome(turnID, refs, false), nil
}

func sshStageOutcome(turnID int64, refs []sshStageRef, replay bool) ToolOutcome {
	raw, _ := json.Marshal(map[string]any{"status": "staged", "turn_id": turnID, "staged_count": len(refs), "items": refs, "already_staged": replay, "message": "SSH 凭据已加密暂存，尚未登记；请对每个 intake_id 调用 ssh_server_register"})
	return ToolOutcome{Content: string(raw)}
}
