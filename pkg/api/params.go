package api

// Shared parameter types. These are declared once and referenced by several
// actions, so a caller learns "a mailbox is {account, mailbox}" a single time
// and the schema describes it once under defs.

// MailboxRef names one mailbox inside one account. Both fields are required;
// the pair is what makes a mailbox unambiguous, because names like "INBOX" and
// "All Mail" repeat across accounts.
type MailboxRef struct {
	Account string `json:"account" req:"1" desc:"account name, e.g. \"Exchange\""`
	Mailbox string `json:"mailbox" req:"1" desc:"mailbox name inside that account, e.g. \"收件箱\""`
}

// MessageInMailbox identifies a single message. Mail.app needs the mailbox the
// message currently lives in; ids alone are only enough for the actions that
// resolve them globally.
type MessageInMailbox struct {
	ID string     `json:"id" req:"1" desc:"Mail.app message id"`
	In MailboxRef `json:"in" req:"1" desc:"the mailbox the message is currently in"`
}

// Recipients groups the address fields of a message. At least one To recipient
// is required; Cc and Bcc are optional.
type Recipients struct {
	To  []string `json:"to" req:"1" desc:"To recipients"`
	Cc  []string `json:"cc,omitempty" desc:"Cc recipients"`
	Bcc []string `json:"bcc,omitempty" desc:"Bcc recipients"`
}

// StateSet selects the flags to set on a message. A field left out is left
// alone, so {"read":true} marks read without touching the flag.
type StateSet struct {
	Read    *bool `json:"read,omitempty" desc:"true marks read, false marks unread"`
	Flagged *bool `json:"flagged,omitempty" desc:"true flags, false unflags"`
}

// Empty reports whether the caller asked for no state change at all.
func (s StateSet) Empty() bool { return s.Read == nil && s.Flagged == nil }

// MarkSelect is a discriminated union: Mode decides which of the remaining
// fields are meaningful, and the fields of the other mode are rejected. This
// replaces the old flat form, where --mailbox together with --all was
// expressible and had to be rejected at runtime.
type MarkSelect struct {
	Mode     string   `json:"mode" req:"1" enum:"mailbox|special" desc:"mailbox: one named mailbox; special: the provider-independent Trash/Junk/Archive across accounts"`
	Account  string   `json:"account,omitempty" desc:"mode=mailbox: the account that owns the mailbox (required)"`
	Mailbox  string   `json:"mailbox,omitempty" desc:"mode=mailbox: the mailbox to mark (required)"`
	Kinds    []string `json:"kinds,omitempty" enum:"trash|junk|archive" desc:"mode=special: which mailboxes to cover (required)"`
	Accounts []string `json:"accounts,omitempty" desc:"mode=special: limit to these accounts; omit for every account"`
}

// typeNotes documents constraints that reflection cannot express. They are
// attached to the matching def in the schema output.
var typeNotes = map[string]string{
	"MarkSelect": "mode=mailbox requires account and mailbox and rejects kinds/accounts; " +
		"mode=special requires kinds and rejects account/mailbox.",
	"StateSet": "at least one of read/flagged must be present.",
}
