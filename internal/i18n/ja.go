package i18n

// Mail body placeholders: hold (title, quantity, deadline, url), purchased
// (title, date, seat number, url), refund (title, amount, url).
var jaStrings = map[string]string{
	SiteTitle:   "firstline",
	NavEvents:   "イベント一覧",
	NavLookup:   "購入の照会",
	NavAdmin:    "主催者",
	ActionBack:  "戻る",
	LabelEmail:  "メールアドレス",
	LabelQty:    "枚数",
	LabelStatus: "状態",
	LabelDate:   "開催日時",
	LabelPrice:  "価格",
	LabelRemain: "残数",
	LabelTitle:  "題名",

	ListHeading:   "イベント一覧",
	ListEmpty:     "いま販売中のイベントはありません。",
	ListNotOnSale: "販売前",
	ListSoldOut:   "売り切れ",

	DetailBuy:          "購入する",
	DetailQuantityHelp: "1回に買えるのは1枚から4枚までです。",

	StatusHeading:      "購入の状況",
	StatusHeld:         "決済待ち",
	StatusPurchased:    "購入済み",
	StatusRefunding:    "払い戻し中",
	StatusRefunded:     "払い戻し済み",
	StatusExpired:      "期限切れ",
	StatusFailed:       "決済に失敗しました",
	StatusSeatNumber:   "整理番号",
	StatusDeadline:     "決済の期限",
	StatusDeadlineNote: "期限までに決済を終えないと、席は確保されません。",
	StatusContinue:     "決済を続ける",

	LookupHeading:  "購入の照会",
	LookupToken:    "照会トークン",
	LookupSubmit:   "照会する",
	LookupMismatch: "メールアドレスまたはトークンが一致する購入が見つかりません。",

	AdminLoginHeading: "主催者ログイン",
	AdminPassword:     "パスワード",
	AdminLoginSubmit:  "ログイン",
	AdminLoginFailed:  "メールアドレスまたはパスワードが違います。",
	AdminEventsHead:   "自分のイベント",
	AdminNewEvent:     "イベントを作る",
	AdminCreateHead:   "イベントの作成",
	AdminTitleJa:      "題名（日本語）",
	AdminTitleEn:      "題名（英語）",
	AdminCapacity:     "定員",
	AdminSaleStart:    "販売開始日時",
	AdminCreateSubmit: "作成する",
	AdminSalesHeading: "販売状況",
	AdminHeld:         "保留中",
	AdminPurchased:    "購入済み",
	AdminRefunds:      "払い戻しの件数",
	AdminLogout:       "ログアウト",

	MailHoldSubject: "【firstline】席を保留しました",
	MailHoldBody: "%s の席を%d枚、保留しました。\n" +
		"決済の期限は %s です。期限までに決済を終えないと、席は確保されません。\n" +
		"購入の状況はこちらで確かめられます: %s\n",
	MailHoldCheckout:     "決済を続けるにはこちらを開いてください: %s\n",
	MailPurchasedSubject: "【firstline】%s の購入が確定しました",
	MailPurchasedBody: "%s の購入が確定しました。\n" +
		"開催日時: %s\n整理番号: %s\n" +
		"購入の状況はこちらで確かめられます: %s\n",
	MailRefundSubject: "【firstline】%s の払い戻しを始めました",
	MailRefundBody: "%s の払い戻しを始めました。\n" +
		"払い戻す金額: %s\n" +
		"購入の状況はこちらで確かめられます: %s\n",

	ErrSoldOut:            "売り切れました。",
	ErrInvalidTransition:  "この購入は、いまの状態ではその操作ができません。",
	ErrNotOnSale:          "このイベントはいま販売していません。",
	ErrBadQuantity:        "枚数は1枚から4枚までで指定してください。",
	ErrDuplicateHold:      "このメールアドレスで保留中の購入がすでにあります。",
	ErrNotFound:           "見つかりませんでした。",
	ErrPaymentUnavailable: "決済を始められませんでした。しばらくしてからやり直してください。",
	ErrInternal:           "エラーが起きました。しばらくしてからやり直してください。",
}
