package rolepanel

// Seed is the setup the bot starts with, from the roles sheet: the groups in
// the order they are posted. It is only used when there is no roles file yet;
// after that the file (and /rolepanel) is the setup.
//
// The IDs were read off a screenshot of the sheet. A role whose ID is wrong is
// found again by its name when the panel is published.
func Seed() []Category {
	const (
		dove       = "\U0001F54A️"
		controller = "\U0001F3AE"
		crescnt    = "☪️"
		cook       = "\U0001F9D1‍\U0001F373"
	)
	return []Category{
		{Key: "general", Name: "General", Title: DefaultTitle("General"), Roles: []Role{
			{ID: "1321422045648785470", Name: "Tamu Penginapan", Emoji: "👤", Desc: "Tamu terhormat"},
			{ID: "1280167842117062726", Name: "Penghuni Penginapan", Emoji: "🌸", Desc: "The Yuuraid"},
		}},
		{Key: "identity", Name: "Identity", Title: DefaultTitle("Identity"), One: true, Roles: []Role{
			{ID: "1308107745748193281", Name: "Male", Emoji: "🐺", Desc: "Orang-orang Sigma"},
			{ID: "1308107827247845467", Name: "Female", Emoji: dove, Desc: "Kakak-kakak cantik"},
			{ID: "1308107890610933781", Name: "Femboy", Emoji: "🦄", Desc: "Orang stres"},
		}},
		{Key: "interest", Name: "Interest", Title: DefaultTitle("Interest"), Roles: []Role{
			{ID: "1335944261526622299", Name: "Tukang Gambar", Emoji: "🎨", Desc: "Para Artist keren"},
			{ID: "1335942490137694268", Name: "Professor Meme", Emoji: "🤡", Desc: "Chronic doomscroller"},
			{ID: "1386238816184172544", Name: "Kang Masak", Emoji: cook, Desc: "Let him cook"},
			{ID: "1386242055776239707", Name: "Karbit", Emoji: "🪨", Desc: "Untuk pembelok"},
			{ID: "1386242203432652831", Name: "Gamer", Emoji: controller, Desc: "Ingfo mabar"},
		}},
		{Key: "interest2", Name: "Interest 2", Title: DefaultTitle("Interest 2"), Roles: []Role{
			{ID: "1382633877184446615", Name: "DnD Yuuraid", Emoji: "🧙", Desc: "Agenda DnD"},
			{ID: "1426488949366587424", Name: "Yuunobar", Emoji: "🍿", Desc: "Agenda nobar bareng"},
			{ID: "1386242875737505802", Name: "Ahli literatur", Emoji: "📜", Desc: "Biar gk minim literasi"},
			{ID: "1473321753060048956", Name: "YuuKhatam", Emoji: crescnt, Desc: "Bismillah berkah"},
		}},
		{Key: "information", Name: "Information", Title: DefaultTitle("Information"), Roles: []Role{
			{ID: "1568875003481563198", Name: "Youtube Pings", Emoji: "🔔", Desc: "Absen stream"},
			{ID: "1568875470546538606", Name: "Twitter Pings", Emoji: "🔔", Desc: "Stalk twitter"},
		}},
	}
}
