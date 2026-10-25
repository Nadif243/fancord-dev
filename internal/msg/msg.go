// Package msg holds every piece of text the bot sends, so wording is edited in one place and nowhere else.
//
//   - Fan-facing text is Indonesian (decided with the fancord head).
//   - Ops text is English and only ever goes to the private dev/ops channel.
package msg

import (
	"fmt"
	"time"
)

// ---- Fan-facing (Indonesian) -------------------------------------------

const (
	PingDescription = "Cek apakah bot masih hidup"
	UnknownCommand  = "Perintah ini belum dikenal."
	CommandFailed   = "Maaf, ada yang salah. Coba lagi sebentar lagi."

	// Dev-only command texts.
	DevOnly                   = "Perintah ini hanya bisa dipakai di channel dev."
	DevStreamDescription      = "Simulasikan stream palsu (khusus dev)"
	DevStreamStateDescription = "Status stream yang ingin disimulasikan"

	DevCheckDescription        = "Cek stream terbaru di YouTube tanpa mengumumkan (khusus dev)"
	DevCheckChannelDescription = "ID channel YouTube (UC...); kosong = channel Yuura"
	DevCheckPagesDescription   = "Jumlah halaman yang dicek, 50 video per halaman (1-5, bawaan 1)"
	DevCheckNoKey              = "YouTube belum diatur. Isi YOUTUBE_API_KEY di .env lalu jalankan ulang bot."
	DevCheckNoChannel          = "Belum ada channel. Isi YOUTUBE_CHANNEL_ID di .env atau isi opsi channel."
)

// StreamUpcoming announces a scheduled stream. The start time is shown twice: as a full date and as "in 2 hours", both rendered by Discord for each viewer.
func StreamUpcoming(title, url string, start time.Time) string {
	if start.IsZero() {
		return fmt.Sprintf("⏰ **Jadwal stream**: %s\n%s", title, url)
	}
	return fmt.Sprintf("⏰ **Jadwal stream**: %s\nMulai %s (%s)\n%s", title, stampFull(start), stamp(start), url)
}

func StreamLive(title, url string) string {
	return fmt.Sprintf("🔴 **Sedang live!** %s\n%s", title, url)
}

func StreamEnded(title, url string) string {
	return fmt.Sprintf("⚫ **Stream selesai**: %s\n%s", title, url)
}

// DevStreamSet is the reply to /devstream.
func DevStreamSet(state string) string {
	return fmt.Sprintf("Stream dev sekarang: %s", state)
}

// DevCheckFailed is shown when YouTube could not be read. The detail comes from the youtube package, which never includes the API key.
func DevCheckFailed(detail string) string {
	return "Gagal mengecek YouTube: " + detail
}

func DevCheckNone(checked int) string {
	return fmt.Sprintf("Dari %d video terbaru, tidak ada yang stream.", checked)
}

// DevCheckSummary shows the totals, so a count can be compared with YouTube's own Live tab at a glance.
func DevCheckSummary(checked, upcoming, live, ended int) string {
	return fmt.Sprintf("Dari %d video terbaru, %d adalah stream (⏰ %d · 🔴 %d · ⚫ %d):",
		checked, upcoming+live+ended, upcoming, live, ended)
}

// DevCheckLine is one stream in the /devcheck answer. icon says its state.
func DevCheckLine(icon, title string, start time.Time) string {
	title = shorten(title, 60)
	if start.IsZero() {
		return fmt.Sprintf("%s %s", icon, title)
	}
	return fmt.Sprintf("%s %s · %s", icon, title, stamp(start))
}

func DevCheckMore(n int) string {
	return fmt.Sprintf("…dan %d lainnya", n)
}

// shorten cuts s to at most max characters (not bytes, so no emoji or Japanese character is ever cut in half).
func shorten(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// Pong answers /ping. The gateway latency is 0 until Discord has answered the first heartbeat, so it is left out then.
func Pong(latency time.Duration) string {
	if latency <= 0 {
		return "Pong! Bot hidup."
	}
	return fmt.Sprintf("Pong! Bot hidup. Latensi gateway: %d ms.", latency.Milliseconds())
}

// ---- Ops (English, dev only) ----------------------------------------------

// stamp renders a Discord timestamp, which every viewer sees as a live relative time such as "2 minutes ago".
func stamp(t time.Time) string { return fmt.Sprintf("<t:%d:R>", t.Unix()) }

// stampFull is the same idea with the "f" style: a full date and time.
func stampFull(t time.Time) string { return fmt.Sprintf("<t:%d:f>", t.Unix()) }

// HeartbeatOnline is the text of the one message the heartbeat keeps editing.
func HeartbeatOnline(version string, started, now time.Time) string {
	return fmt.Sprintf("**Online** · v%s\nStarted %s · last beat %s", version, stamp(started), stamp(now))
}

// HeartbeatOffline replaces it on a clean shutdown. If the message is stuck on "Online" with an old "last beat", the bot died without shutting down.
func HeartbeatOffline(version string, now time.Time) string {
	return fmt.Sprintf("**Offline** · v%s\nStopped cleanly %s", version, stamp(now))
}
