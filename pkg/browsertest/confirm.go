package browsertest

import (
	"context"
	"strconv"

	"github.com/chromedp/chromedp"
)

// pkg/confirm asks its questions in a dialog of its own on the page
// (#confirm-dialog), never window.confirm, so a suite answers one as a
// person does: it waits for the question, reads it, and presses one of
// the dialog's two buttons. Agree and Refuse are those acts; each waits
// for the question it answered to close, though another may be asking
// already. An agreed submit goes on in the page, and whatever it does
// next is the suite's to wait for.

// Agree waits for the page's confirmation dialog, reads its question
// into asked (when asked is not nil), and presses its action.
func Agree(asked *string) chromedp.Action { return answerConfirm(true, asked) }

// Refuse waits for the page's confirmation dialog, reads its question
// into asked (when asked is not nil), and presses Cancel.
func Refuse(asked *string) chromedp.Action { return answerConfirm(false, asked) }

func answerConfirm(yes bool, asked *string) chromedp.Action {
	if asked == nil {
		asked = new(string)
	}
	button := "#confirm-dialog[open] .confirm-dialog-cancel"
	if yes {
		// The action is armed a moment after the question shows, so a
		// tap that asked can't also answer it.
		button = "#confirm-dialog[open][data-armed] .confirm-dialog-act"
	}
	var question string
	return chromedp.Tasks{
		Text(`#confirm-dialog[open] .confirm-dialog-question`, asked),
		chromedp.Evaluate(`document.querySelector('#confirm-dialog').dataset.asked`, &question),
		Click(button),
		// This question closes — another may already be asking, and an
		// agreed submit may have taken the page, dialog and all.
		chromedp.ActionFunc(func(ctx context.Context) error {
			return Poll(`(d => !d || !d.open || d.dataset.asked !== ` + strconv.Quote(question) + `)(document.querySelector('#confirm-dialog'))`).Do(ctx)
		}),
	}
}
