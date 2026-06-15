package multistore
import ("fmt"; "github.com/google/uuid"; "gorm.io/gorm"; "github.com/JingxuanC/vela-engine/internal/model")
type GroupManager struct { db *gorm.DB }
func NewGroupManager(db *gorm.DB) *GroupManager { return &GroupManager{db} }
func (m *GroupManager) CreateGroup(ownerID, name string) (*model.StoreGroup, error) {
    g := &model.StoreGroup{OwnerShopID: uuid.MustParse(ownerID), Name: name, MemberCount: 1, IsActive: true}
    if err := m.db.Create(g).Error; err != nil { return nil, err }
    m.db.Create(&model.StoreGroupMember{GroupID: g.ID, ShopID: g.OwnerShopID, Role: "owner"})
    return g, nil
}
func (m *GroupManager) AddStore(groupID, requesterID, targetID, role string) error {
    var g model.StoreGroup
    if err := m.db.First(&g, "id=?", groupID).Error; err != nil { return fmt.Errorf("group not found") }
    parsed, err := uuid.Parse(requesterID)
    if err != nil { return fmt.Errorf("invalid shop_id") }
    if g.OwnerShopID != parsed { return fmt.Errorf("only owner can add") }
    m.db.Create(&model.StoreGroupMember{GroupID: g.ID, ShopID: uuid.MustParse(targetID), Role: role})
    m.db.Model(&g).UpdateColumn("member_count", gorm.Expr("member_count+1"))
    return nil
}
func (m *GroupManager) RemoveStore(groupID, requesterID, targetID string) error {
    var g model.StoreGroup
    if err := m.db.First(&g, "id=?", groupID).Error; err != nil { return fmt.Errorf("group not found") }
    parsed, err := uuid.Parse(requesterID)
    if err != nil { return fmt.Errorf("invalid shop_id") }
    if g.OwnerShopID != parsed { return fmt.Errorf("only owner can remove") }
    m.db.Where("group_id=? AND shop_id=?", groupID, targetID).Delete(&model.StoreGroupMember{})
    m.db.Model(&g).UpdateColumn("member_count", gorm.Expr("member_count-1"))
    return nil
}
func (m *GroupManager) GetGroupWithMembers(gid string) (*model.StoreGroup, error) {
    var g model.StoreGroup
    if err := m.db.Preload("Members").First(&g, "id=?", gid).Error; err != nil { return nil, err }
    return &g, nil
}
func (m *GroupManager) GetGroupsForShop(sid string) ([]model.StoreGroup, error) {
    var gs []model.StoreGroup
    m.db.Joins("JOIN store_group_members ON store_group_members.group_id=store_groups.id").Where("store_group_members.shop_id=?", sid).Preload("Members").Find(&gs)
    return gs, nil
}
func (m *GroupManager) VerifyMembership(sid1, sid2 string) (string, bool, error) {
    var r struct{ GroupID string }
    m.db.Table("store_group_members a").Select("a.group_id").Joins("JOIN store_group_members b ON b.group_id=a.group_id").Where("a.shop_id=? AND b.shop_id=?", sid1, sid2).Limit(1).Scan(&r)
    return r.GroupID, r.GroupID != "", nil
}
