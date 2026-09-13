package ledger

const (
	OwnerUser     = "user"
	OwnerMerchant = "merchant"
	OwnerPlatform = "platform"
	OwnerProvider = "provider"

	TypeUserWallet       = "user_wallet"
	TypeUserHold         = "user_hold"
	TypeMerchantPayable  = "merchant_payable"
	TypeMerchantHoldback = "merchant_holdback"
	TypeFeeRevenue       = "platform_fee_revenue"
	TypeFloat            = "platform_float"
	TypeClearing         = "provider_clearing"
)

func AccountID(ownerType, ownerID, accountType string) string {
	if ownerID == "" {
		return "acc_" + ownerType + "_" + accountType
	}
	return "acc_" + ownerID + "_" + accountType
}

func UserWallet(userID string) string {
	return AccountID(OwnerUser, userID, TypeUserWallet)
}

func MerchantPayable(merchantID string) string {
	return AccountID(OwnerMerchant, merchantID, TypeMerchantPayable)
}

func MerchantHoldback(merchantID string) string {
	return AccountID(OwnerMerchant, merchantID, TypeMerchantHoldback)
}

func PlatformFeeRevenue() string {
	return AccountID(OwnerPlatform, "", TypeFeeRevenue)
}

func PlatformFloat() string {
	return AccountID(OwnerPlatform, "", TypeFloat)
}
