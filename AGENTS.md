# go-xsapi Guidance

- When changing Xbox Live, MPSD, or session lifecycle/status-code behavior, verify against the official Microsoft GDK docs first instead of inferring protocol behavior from existing code.
- Start from the GDK reference TOC:
  [Microsoft GDK Live Reference](https://learn.microsoft.com/en-us/gaming/gdk/docs/reference/live/gc-reference-live-toc?view=gdk-2510)
- Prefer table-driven tests when multiple cases exercise the same behavior; keep single-case tests straightforward when a table would add noise.
- Use the official open-sourced C++ implementation as a reference, but note that
  not all logic should be taken literally due to differences with our use case.
  https://github.com/microsoft/xbox-live-api
