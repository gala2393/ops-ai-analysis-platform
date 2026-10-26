package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"yunwei/internal/store"
)

// maxFeedbackNote bounds the optional feedback note.
const maxFeedbackNote = 500

// feedbackRequest is the JSON body of POST /api/feedback.
type feedbackRequest struct {
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
	Rating     string `json:"rating"`
	Note       string `json:"note"`
}

// feedbackItem is one stored feedback entry rendered in the widget.
type feedbackItem struct {
	Rating     string
	RatingText string
	Note       string
	Time       string
}

// feedbackView is the model of the feedback widget (page and fragment).
type feedbackView struct {
	ElementID  string
	TargetType string
	TargetID   string
	Items      []feedbackItem
}

// newFeedbackView builds the widget model for one target.
func newFeedbackView(targetType, targetID string, stored []*store.Feedback) feedbackView {
	v := feedbackView{
		ElementID:  "fb-" + targetType + "-" + strings.NewReplacer(":", "-", "/", "-", ".", "-").Replace(targetID),
		TargetType: targetType,
		TargetID:   targetID,
	}
	for _, f := range stored {
		v.Items = append(v.Items, feedbackItem{
			Rating: f.Rating, RatingText: ratingText(f.Rating), Note: f.Note, Time: fmtTime(f.CreatedAt),
		})
	}
	return v
}

// ratingText renders a feedback rating in Chinese.
func ratingText(rating string) string {
	switch rating {
	case store.RatingUseful:
		return "有用"
	case store.RatingUseless:
		return "没用"
	case store.RatingWrong:
		return "错误"
	}
	return rating
}

// apiFeedback handles POST /api/feedback. JSON bodies get JSON answers; htmx
// form posts get the refreshed feedback widget fragment. Invalid ratings,
// target types or non-existent targets are rejected with 400.
func (s *Server) apiFeedback(w http.ResponseWriter, r *http.Request) {
	var req feedbackRequest
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/json") {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不是有效的 JSON (invalid JSON body)"})
			return
		}
	} else {
		if err := r.ParseForm(); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "无法解析表单 (invalid form)"})
			return
		}
		req = feedbackRequest{
			TargetType: r.FormValue("target_type"),
			TargetID:   r.FormValue("target_id"),
			Rating:     r.FormValue("rating"),
			Note:       r.FormValue("note"),
		}
	}
	req.Note = strings.TrimSpace(req.Note)

	if !store.ValidRating(req.Rating) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("无效的评分 %q,应为 useful/useless/wrong (invalid rating)", req.Rating)})
		return
	}
	if !store.ValidFeedbackTargetType(req.TargetType) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("无效的目标类型 %q,应为 run_group/alert_event (invalid target type)", req.TargetType)})
		return
	}
	exists, err := s.store.FeedbackTargetExists(r.Context(), req.TargetType, req.TargetID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if !exists {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "反馈目标不存在 (feedback target not found)"})
		return
	}
	if len([]rune(req.Note)) > maxFeedbackNote {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "备注过长 (note too long)"})
		return
	}

	fb := &store.Feedback{TargetType: req.TargetType, TargetID: req.TargetID, Rating: req.Rating, Note: req.Note}
	id, err := s.store.AddFeedback(r.Context(), fb)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		stored, err := s.store.FeedbackFor(r.Context(), req.TargetType, req.TargetID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		s.renderFrag(w, http.StatusOK, "feedback_area", newFeedbackView(req.TargetType, req.TargetID, stored))
		return
	}
	fb.ID = id
	writeJSON(w, http.StatusCreated, fb)
}
