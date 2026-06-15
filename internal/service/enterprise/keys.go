package enterprise

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/JingxuanC/vela-engine/internal/model"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type KeyManager struct {
	db               *gorm.DB
	defaultRateLimit int
}

func NewKeyManager(db *gorm.DB, rl int) *KeyManager { return &KeyManager{db: db, defaultRateLimit: rl} }

type GenerateKeyResult struct {
	FullKey     string
	Prefix      string
	KeyHash     string
	KeyLastFour string
	ApiKey      *model.ApiKey
}

func (m *KeyManager) GenerateKey(shopID, label string, scopes []byte, rateLimit int) (*GenerateKeyResult, error) {
	b := make([]byte, 32)
	rand.Read(b)
	fk := hex.EncodeToString(b)
	prefix := "vla_key_prod_" + fk[:12]
	hash, _ := bcrypt.GenerateFromPassword([]byte(fk), 10)
	last4 := fk[60:]
	if rateLimit <= 0 {
		rateLimit = m.defaultRateLimit
	}
	ak := &model.ApiKey{ShopID: uuid.MustParse(shopID), Label: label, Prefix: prefix, KeyHash: string(hash), KeyLastFour: last4, Scopes: scopes, RateLimit: rateLimit, IsActive: true}
	m.db.Create(ak)
	return &GenerateKeyResult{FullKey: prefix + "_" + fk, Prefix: prefix, KeyHash: string(hash), KeyLastFour: last4, ApiKey: ak}, nil
}
func (m *KeyManager) ValidateKey(fk string) (*model.ApiKey, error) {
	if len(fk) < 30 {
		return nil, fmt.Errorf("invalid key")
	}
	prefix := fk[:len("vla_key_prod_")+12]
	var ak model.ApiKey
	m.db.Where("prefix=?", prefix).First(&ak)
	if err := bcrypt.CompareHashAndPassword([]byte(ak.KeyHash), []byte(fk)); err != nil {
		return nil, fmt.Errorf("invalid key")
	}
	if !ak.IsActive {
		return nil, fmt.Errorf("revoked")
	}
	if ak.ExpiresAt != nil && time.Now().After(*ak.ExpiresAt) {
		return nil, fmt.Errorf("expired")
	}
	now := time.Now()
	m.db.Model(&ak).Update("last_used_at", &now)
	return &ak, nil
}
func (m *KeyManager) RevokeKey(kid, sid string) error {
	return m.db.Model(&model.ApiKey{}).Where("id=? AND shop_id=?", kid, sid).Update("is_active", false).Error
}
func (m *KeyManager) ListKeys(sid string) ([]model.ApiKey, error) {
	var ks []model.ApiKey
	err := m.db.Where("shop_id=?", sid).Order("created_at DESC").Find(&ks).Error
	return ks, err
}
func (m *KeyManager) GetKey(kid, sid string) (*model.ApiKey, error) {
	var k model.ApiKey
	err := m.db.Where("id=? AND shop_id=?", kid, sid).First(&k).Error
	return &k, err
}
