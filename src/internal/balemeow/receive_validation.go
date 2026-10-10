package balemeow

import "github.com/mimalef70/goomni/src/internal/balemeow/wire"

func validateMessage(m *wire.Message) error { budget := 2048; return validateContent(m, 0, &budget) }
func validateContent(m *wire.Message, depth int, budget *int) error {
	if m == nil {
		return nil
	}
	if depth >= maxContentDepth || *budget <= 0 {
		return protocolError()
	}
	*budget--
	count := 0
	for _, v := range []bool{m.Json != nil, m.Text != nil, m.Document != nil, m.Empty != nil, m.Service != nil, m.Sticker != nil, m.Template != nil, m.TemplateResponse != nil, m.Gift != nil, m.Unsupported != nil, m.Poll != nil, m.GoldGift != nil, m.AnimatedSticker != nil} {
		if v {
			count++
		}
	}
	if count > 1 {
		return protocolError()
	}
	if t := m.Text; t != nil && len(t.Mentions) > 4096 {
		return protocolError()
	}
	if d := m.Document; d != nil {
		if d.FileId == 0 || d.FileSize < 0 {
			return protocolError()
		}
		if d.Caption != nil && len(d.Caption.Mentions) > 4096 {
			return protocolError()
		}
	}
	if t := m.Template; t != nil {
		count := len(t.Buttons)
		for _, b := range t.Buttons {
			if b == nil {
				return protocolError()
			}
		}
		if t.InlineKeyboard != nil {
			for _, row := range t.InlineKeyboard.Rows {
				if row == nil {
					return protocolError()
				}
				count += len(row.Buttons)
				for _, b := range row.Buttons {
					if b == nil {
						return protocolError()
					}
				}
			}
		}
		if t.ReplyKeyboard != nil {
			for _, row := range t.ReplyKeyboard.Rows {
				if row == nil {
					return protocolError()
				}
				count += len(row.Buttons)
				for _, b := range row.Buttons {
					if b == nil {
						return protocolError()
					}
				}
			}
		}
		if count > 1024 || count > *budget {
			return protocolError()
		}
		*budget -= count
		if e := validateContent(t.Message, depth+1, budget); e != nil {
			return e
		}
	}
	if t := m.TemplateResponse; t != nil {
		if e := validateContent(t.Message, depth+1, budget); e != nil {
			return e
		}
	}
	images := []*wire.StickerImage{}
	if st := m.Sticker; st != nil {
		if st.Format < 0 {
			return protocolError()
		}
		images = append(images, st.Image512, st.Image256, st.Animation)
	}
	if st := m.AnimatedSticker; st != nil {
		images = append(images, st.FileLocation)
	}
	for _, img := range images {
		if img != nil && (img.Width < 0 || img.Height < 0 || img.FileSize < 0 || (img.File != nil && img.File.FileId == 0)) {
			return protocolError()
		}
	}
	if g := m.Gift; g != nil && (g.Count < 0 || g.TotalAmount < 0) {
		return protocolError()
	}
	if g := m.GoldGift; g != nil && (g.Amount < 0 || g.Count < 0) {
		return protocolError()
	}
	if p := m.Poll; p != nil {
		if len(p.Options) > 100 || p.Type < 0 || p.Type > 1 {
			return protocolError()
		}
		for _, o := range p.Options {
			if o == nil || o.Id < 0 {
				return protocolError()
			}
		}
		if r := p.Result; r != nil {
			if r.VotersCount < 0 || len(r.OptionResults) > 100 || len(r.RecentVoters) > 4096 || len(r.ChosenOptionIds) > 100 {
				return protocolError()
			}
			for _, v := range r.OptionResults {
				if v == nil || v.OptionId < 0 || v.VotesCount < 0 {
					return protocolError()
				}
			}
		}
	}
	return nil
}
func validateQuote(q *wire.QuotedMessage) error {
	if q == nil {
		return nil
	}
	if q.Date < 0 || (q.MessageId != nil && q.MessageId.Value == 0) {
		return protocolError()
	}
	if q.Peer != nil {
		if _, e := decodePeer(q.Peer); e != nil {
			return e
		}
	}
	return validateMessage(q.Message)
}
func validateHistoryItem(m *wire.HistoryItem) error {
	if m == nil || m.Rid == 0 || m.Date <= 0 || m.SenderId == 0 {
		return protocolError()
	}
	if e := validateMessage(m.Message); e != nil {
		return e
	}
	return validateQuote(m.QuotedMessage)
}
