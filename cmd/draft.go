package cmd

import (
	"encoding/json"
	"fmt"
	netmail "net/mail"
	"os"
	"strings"

	"github.com/intelligrit/mail-app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

var (
	draftAccount     string
	draftFrom        string
	draftTo          []string
	draftCc          []string
	draftBcc         []string
	draftSubject     string
	draftBody        string
	draftAttachments []string
)

var draftCmd = &cobra.Command{
	Use:   "draft",
	Short: "Compose a message as a draft (never sends)",
	Long: `Compose a message and save it into Mail.app's Drafts mailbox. Nothing is sent:
you review the draft in Mail.app and press Send there yourself.

This binary intentionally has no command that sends mail.

Examples:
  mail-app-cli draft -a "Google" -t user@example.com -s "Hello" --body "Message content"
  mail-app-cli draft -f "user@example.com" -t user@example.com -s "Hello" --body "..."
  mail-app-cli draft -a "Google" -t a@x.com -c b@x.com -s "With attachment" --body "..." --attach ~/f.pdf`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(draftTo) == 0 {
			return fmt.Errorf("at least one --to recipient is required")
		}
		if draftSubject == "" {
			return fmt.Errorf("--subject is required")
		}

		client := mail.NewClient()

		// If draftAccount is not specified, try to infer it from draftFrom or a
		// single configured account.
		if draftAccount == "" {
			var rawEmail string
			if draftFrom != "" {
				if addr, err := netmail.ParseAddress(draftFrom); err == nil {
					rawEmail = strings.ToLower(strings.TrimSpace(addr.Address))
				} else {
					rawEmail = strings.ToLower(strings.Trim(strings.TrimSpace(draftFrom), "<>"))
				}
			}

			accounts, err := client.GetAccountsJSON()
			if err == nil {
				if rawEmail != "" {
					for _, acc := range accounts {
						if strings.ToLower(acc.EmailAddress) == rawEmail {
							draftAccount = acc.Name
							break
						}
						for _, alias := range acc.EmailAddresses {
							if strings.ToLower(alias) == rawEmail {
								draftAccount = acc.Name
								break
							}
						}
						if draftAccount != "" {
							break
						}
					}
				}
				if draftAccount == "" && len(accounts) == 1 {
					draftAccount = accounts[0].Name
				}
			}
		}

		if draftAccount == "" {
			return fmt.Errorf("--account is required (or specify --from matching a configured account)")
		}

		result, err := client.SaveDraft(draftAccount, draftFrom, draftSubject, draftBody,
			draftTo, draftCc, draftBcc, draftAttachments)
		if err != nil {
			return fmt.Errorf("failed to save draft: %w", err)
		}

		output, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal draft result: %w", err)
		}
		fmt.Println(string(output))

		if result.Verified {
			fmt.Fprintf(os.Stderr,
				"Draft %s saved to the %s Drafts mailbox (nothing was sent). Review it in Mail.app and press Send there.\n",
				result.ID, draftAccount)
		} else {
			fmt.Fprintf(os.Stderr,
				"Draft composed for the %s account (nothing was sent), but it was not visible in the Drafts mailbox yet. Check Mail.app before relying on it.\n",
				draftAccount)
		}
		for _, warning := range result.Warnings {
			fmt.Fprintf(os.Stderr, "warning: %s\n", warning)
		}
		return nil
	},
}

func init() {
	draftCmd.Flags().StringVarP(&draftAccount, "account", "a", "", "Account to draft from (inferred from --from if omitted)")
	draftCmd.Flags().StringVarP(&draftFrom, "from", "f", "", "From address or sender (e.g. \"Name <addr@example.com>\" or \"addr@example.com\")")
	draftCmd.Flags().StringSliceVarP(&draftTo, "to", "t", []string{}, "To recipients (can be specified multiple times)")
	draftCmd.Flags().StringSliceVarP(&draftCc, "cc", "c", []string{}, "Cc recipients (can be specified multiple times)")
	draftCmd.Flags().StringSliceVarP(&draftBcc, "bcc", "b", []string{}, "Bcc recipients (can be specified multiple times)")
	draftCmd.Flags().StringVarP(&draftSubject, "subject", "s", "", "Draft subject (required)")
	draftCmd.Flags().StringVar(&draftBody, "body", "", "Draft body content")
	draftCmd.Flags().StringSliceVar(&draftAttachments, "attach", []string{}, "File paths to attach (can be specified multiple times)")
}
