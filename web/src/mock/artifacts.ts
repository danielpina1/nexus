export const diffText = `diff --git a/backend/internal/events/stream.go b/backend/internal/events/stream.go
--- a/backend/internal/events/stream.go
+++ b/backend/internal/events/stream.go
@@ -41,18 +41,27 @@ func (s *Stream) Subscribe(ctx context.Context, since int64, send func(*Event) error) error {
-	// Replay history, then attach to live events.
-	history, err := s.store.After(ctx, since)
-	if err != nil {
-		return err
-	}
-	for _, ev := range history {
-		if err := send(ev); err != nil {
-			return err
-		}
-	}
-	live := s.bus.Listen()
+	// Attach first so nothing published during the replay query is lost,
+	// then replay history and drop duplicates by seq.
+	live := s.bus.Listen()
 	defer s.bus.Unlisten(live)
+	history, err := s.store.After(ctx, since)
+	if err != nil {
+		return err
+	}
+	last := since
+	for _, ev := range history {
+		if err := send(ev); err != nil {
+			return err
+		}
+		last = ev.Seq
+	}
 	for {
 		select {
 		case ev := <-live:
+			if ev.Seq <= last {
+				continue
+			}
+			last = ev.Seq
 			if err := send(ev); err != nil {
 				return err
 			}
`;

export function cartSvg(items: number, totalMinor: number): string {
  const rows = [
    ["Leite Mimosa meio-gordo 1L", "6×", "€32.04"],
    ["Café Delta Lote Chávena", "2×", "€15.96"],
    ["Salmão fresco (posta)", "2×", "€23.96"],
    ["Azeite Gallo virgem extra", "1×", "€8.99"],
    ["… and " + (items - 4) + " more", "", ""],
  ];
  const r = rows
    .map(
      ([a, b, c], i) =>
        `<text x="24" y="${118 + i * 30}" font-size="14" fill="#1f2937">${a}</text><text x="380" y="${118 + i * 30}" font-size="14" fill="#6b7280">${b}</text><text x="456" y="${118 + i * 30}" font-size="14" text-anchor="end" fill="#111827">${c}</text>`,
    )
    .join("");
  return `<svg xmlns="http://www.w3.org/2000/svg" width="480" height="300" viewBox="0 0 480 300" font-family="Helvetica, Arial, sans-serif">
<rect width="480" height="300" rx="12" fill="#ffffff"/>
<rect width="480" height="56" rx="12" fill="#e30613"/><rect y="40" width="480" height="16" fill="#e30613"/>
<text x="24" y="36" font-size="20" font-weight="700" fill="#fff">Continente · Carrinho</text>
<text x="456" y="36" font-size="14" text-anchor="end" fill="#fff">${items} artigos</text>
<text x="24" y="86" font-size="12" fill="#6b7280">Entrega: Sábado 9h–11h · Casa</text>
${r}
<line x1="24" x2="456" y1="262" y2="262" stroke="#e5e7eb"/>
<text x="24" y="286" font-size="16" font-weight="700" fill="#111827">Total</text>
<text x="456" y="286" font-size="16" font-weight="700" text-anchor="end" fill="#111827">€${(totalMinor / 100).toFixed(2)}</text>
</svg>`;
}
