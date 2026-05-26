package gemini

import "fmt"

// DefaultIdentifyPrompt is the same prompt the Mac and Android
// clients use. Kept in sync because all three clients share the
// TITLE/NOTES/TRANSCRIPT parser — any change to the marker shape
// here MUST land in stash-mac (AIProvider.swift) and droid_stash
// (GeminiClient.kt) at the same time, or one platform's output
// stops parsing.
const DefaultIdentifyPrompt = `Identify the main subject in this photo.

Respond with exactly these three lines, no preamble, no markdown:

TITLE: <common name; include scientific name in parentheses when applicable>
NOTES: <natural prose, three to six sentences. Open by naming the subject in plain language — e.g. "This is the YYYY mushroom (Scientificus nameus), also known as XXXX..." or "This is the eastern bluebird (Sialia sialis), a small thrush native to..." Then cover, where relevant: notable visual characteristics; habitat, range, or season; edibility / toxicity / safety; species commonly confused with it; what specific features visible in this photo helped identify it; and any other interesting facts a curious naturalist would want to know. Be generous with detail — the user will trim what they don't want.>
TRANSCRIPT: <if the photo contains readable text — printed, typed, OR handwritten (including cursive) — transcribe it verbatim here, preserving line breaks where they're meaningful. Cover the entire visible text, not just a sample. If the image contains no meaningful text (e.g. it's a flower, animal, landscape with no signs / labels / writing), write exactly NONE.>

If you can't identify confidently, write TITLE: Unknown and explain your best guess and the reasoning in NOTES.`

// MultiImageHint is prepended to the prompt when an identify call
// carries more than one image — tells Gemini they're the same
// subject from different angles/states, not N unrelated subjects.
// Without this, a multi-angle mushroom shot comes back as "I see
// three different fungi" instead of one identification.
func MultiImageHint(count int) string {
	return fmt.Sprintf(
		"The following %d photos are of the same subject from different angles or states. Identify the subject using all photos together.\n\n",
		count,
	)
}
