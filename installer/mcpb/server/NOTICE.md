# Third-party components

This extension redistributes the following binaries: `whisper-cli` and `lib/`
on macOS, `whisper-cli.exe` and the `.dll` files beside it on Windows.

## whisper.cpp — MIT License

Copyright (c) 2023-2024 The ggml authors

Permission is hereby granted, free of charge, to any person obtaining a copy of
this software and associated documentation files (the "Software"), to deal in
the Software without restriction, including without limitation the rights to
use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies
of the Software, and to permit persons to whom the Software is furnished to do
so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

Applies to: `whisper-cli`, `lib/libwhisper.*.dylib`, `whisper-cli.exe`,
`whisper.dll`

## ggml — MIT License

Copyright (c) 2023-2024 The ggml authors

Same MIT terms as above.

Applies to: `lib/libggml*.dylib`, `lib/libggml-*.so`, `ggml.dll`,
`ggml-base.dll`, `ggml-cpu-*.dll`

## LLVM OpenMP Runtime — Apache License 2.0 with LLVM Exception

Copyright (c) LLVM Project contributors.

Licensed under the Apache License, Version 2.0 with the LLVM exception. You may
obtain a copy of the License at <https://llvm.org/LICENSE.txt>.

Applies to: `lib/libomp.dylib` (macOS only)

## Speech model

The speech model (`ggml-large-v3-turbo.bin`) is **not** distributed with this
extension. It is downloaded on first run from
<https://huggingface.co/ggerganov/whisper.cpp>, and is derived from OpenAI's
Whisper, released under the MIT License.
