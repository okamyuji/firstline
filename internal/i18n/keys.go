package i18n

// Every constant here needs a string in both ja.go and en.go; the tests enforce it.
const (
	// Shared chrome.
	SiteTitle   = "site.title"
	NavEvents   = "nav.events"
	NavLookup   = "nav.lookup"
	NavAdmin    = "nav.admin"
	ActionBack  = "action.back"
	LabelEmail  = "label.email"
	LabelQty    = "label.quantity"
	LabelStatus = "label.status"
	LabelDate   = "label.date"
	LabelPrice  = "label.price"
	LabelRemain = "label.remaining"
	LabelTitle  = "label.title"
	PriceYen    = "price.yen"

	// Event list.
	ListHeading   = "list.heading"
	ListEmpty     = "list.empty"
	ListNotOnSale = "list.not_on_sale"
	ListSoldOut   = "list.sold_out"

	// Event detail and purchase form.
	DetailBuy          = "detail.buy"
	DetailQuantityHelp = "detail.quantity_help"

	// Purchase status.
	StatusHeading      = "status.heading"
	StatusHeld         = "status.held"
	StatusPurchased    = "status.purchased"
	StatusRefunding    = "status.refunding"
	StatusRefunded     = "status.refunded"
	StatusExpired      = "status.expired"
	StatusFailed       = "status.failed"
	StatusSeatNumber   = "status.seat_number"
	StatusDeadline     = "status.deadline"
	StatusDeadlineNote = "status.deadline_note"
	StatusContinue     = "status.continue_payment"

	// Lookup.
	LookupHeading  = "lookup.heading"
	LookupToken    = "lookup.token"
	LookupSubmit   = "lookup.submit"
	LookupMismatch = "lookup.mismatch"

	// Organizer screens.
	AdminLoginHeading = "admin.login.heading"
	AdminPassword     = "admin.login.password"
	AdminLoginSubmit  = "admin.login.submit"
	AdminEventsHead   = "admin.events.heading"
	AdminNewEvent     = "admin.events.new"
	AdminCreateHead   = "admin.create.heading"
	AdminTitleJa      = "admin.create.title_ja"
	AdminTitleEn      = "admin.create.title_en"
	AdminCapacity     = "admin.create.capacity"
	AdminSaleStart    = "admin.create.sale_start"
	AdminEndsAt       = "admin.create.ends_at"
	AdminCreateSubmit = "admin.create.submit"
	AdminSalesHeading = "admin.sales.heading"
	AdminHeld         = "admin.sales.held"
	AdminPurchased    = "admin.sales.purchased"
	AdminRefunds      = "admin.sales.refunds"
	AdminLogout       = "admin.logout"

	// Mails.
	MailHoldSubject      = "mail.hold.subject"
	MailHoldBody         = "mail.hold.body"
	MailHoldCheckout     = "mail.hold.checkout"
	MailPurchasedSubject = "mail.purchased.subject"
	MailPurchasedBody    = "mail.purchased.body"
	MailRefundSubject    = "mail.refund.subject"
	MailRefundBody       = "mail.refund.body"

	// Error messages, one per domain error plus a generic one.
	ErrSoldOut            = "error.sold_out"
	ErrInvalidTransition  = "error.invalid_transition"
	ErrNotOnSale          = "error.not_on_sale"
	ErrBadQuantity        = "error.bad_quantity"
	ErrDuplicateHold      = "error.duplicate_hold"
	ErrNotFound           = "error.not_found"
	ErrPaymentUnavailable = "error.payment_unavailable"
	ErrBadInput           = "error.bad_input"
	ErrDuplicateOrganizer = "error.duplicate_organizer"
	ErrBadCredentials     = "error.bad_credentials"
	ErrTooManyAttempts    = "error.too_many_attempts"
	ErrNoSession          = "error.no_session"
	ErrTooManyHolds       = "error.too_many_holds"
	ErrInternal           = "error.internal"
)
