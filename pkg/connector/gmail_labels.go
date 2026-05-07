package connector

import (
	"context"
	"fmt"
	"sort"
	"strings"

	gmail "google.golang.org/api/gmail/v1"

	"github.com/Leicas/matrimail/pkg/imap"
)

// ListGmailLabelsAsFolders enumerates the user's Gmail labels and returns
// them in the same imap.FolderInfo shape the IMAP path produces, so the
// folder-selection UI can present a consistent picker regardless of scope
// mode.
//
// Gmail labels are mapped to imap.FolderInfo as follows:
//
//   - System labels (INBOX, SENT, DRAFT, TRASH, SPAM, IMPORTANT, STARRED,
//     UNREAD, CATEGORY_*) → FolderTypeSystem (with INBOX special-cased to
//     FolderTypeStandard so it sorts first and gets the expected emoji).
//   - User-created labels → FolderTypeLabel.
//
// CATEGORY_* labels are surfaced as system labels but tend to be high-volume
// (Promotions, Social, Forums, etc.) — users typically don't want to bridge
// those, but we present them so the user can opt in if they want.
func ListGmailLabelsAsFolders(ctx context.Context, svc *gmail.Service) ([]imap.FolderInfo, error) {
	if svc == nil {
		return nil, fmt.Errorf("ListGmailLabelsAsFolders: nil gmail.Service")
	}
	resp, err := svc.Users.Labels.List("me").Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("gmail labels.list: %w", err)
	}
	if resp == nil {
		return nil, nil
	}

	folders := make([]imap.FolderInfo, 0, len(resp.Labels))
	for _, lbl := range resp.Labels {
		if lbl == nil || lbl.Id == "" {
			continue
		}
		// Some labels aren't user-visible in the picker even though the API
		// returns them — labels with the "labelListVisibility=labelHide"
		// attribute. The Gmail API does include them in labels.list, so
		// filter here.
		if strings.EqualFold(lbl.LabelListVisibility, "labelHide") {
			continue
		}

		fi := imap.FolderInfo{
			Name:         lbl.Id,   // Gmail label ID — what the inbound poller uses to filter history events
			Display:      lbl.Name, // human-readable, e.g. "INBOX" or "Receipts/Amazon"
			IsSelectable: true,
		}
		switch strings.ToUpper(lbl.Id) {
		case "INBOX":
			fi.Type = imap.FolderTypeStandard
			fi.Icon = "📥"
		case "SENT":
			fi.Type = imap.FolderTypeStandard
			fi.Icon = "📤"
		case "DRAFT":
			fi.Type = imap.FolderTypeStandard
			fi.Icon = "📝"
		case "STARRED":
			fi.Type = imap.FolderTypeSystem
			fi.Icon = "⭐"
		case "IMPORTANT":
			fi.Type = imap.FolderTypeSystem
			fi.Icon = "❗"
		case "TRASH":
			fi.Type = imap.FolderTypeSystem
			fi.Icon = "🗑️"
		case "SPAM":
			fi.Type = imap.FolderTypeSystem
			fi.Icon = "🚫"
		case "UNREAD":
			fi.Type = imap.FolderTypeSystem
			fi.Icon = "🆕"
		default:
			if strings.HasPrefix(strings.ToUpper(lbl.Id), "CATEGORY_") {
				fi.Type = imap.FolderTypeSystem
				fi.Icon = "🗂️"
			} else if strings.EqualFold(lbl.Type, "system") {
				fi.Type = imap.FolderTypeSystem
				fi.Icon = "🔧"
			} else {
				fi.Type = imap.FolderTypeLabel
				fi.Icon = "🏷️"
			}
		}
		folders = append(folders, fi)
	}

	// Stable, useful ordering: standard folders first (INBOX, Sent, Draft),
	// then system, then user labels alphabetically.
	sort.SliceStable(folders, func(i, j int) bool {
		if folders[i].Type != folders[j].Type {
			return folders[i].Type < folders[j].Type
		}
		return strings.ToLower(folders[i].Display) < strings.ToLower(folders[j].Display)
	})
	return folders, nil
}
