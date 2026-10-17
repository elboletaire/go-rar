# libarchive test archives

These RAR4 archives are test fixtures from libarchive, used to check that the
reader handles archives written by WinRAR and other tools, not only this
package's Writer.

- Source: https://github.com/libarchive/libarchive/tree/v3.7.7/libarchive/test
  (files `test_read_format_rar*.rar.uu`, fetched from
  `https://raw.githubusercontent.com/libarchive/libarchive/v3.7.7/libarchive/test/`
  and decoded from uuencoding).
- Tag: v3.7.7
- License: BSD-2-Clause, as stated in the libarchive COPYING file:

    Copyright (c) 2003-2018 <author(s)>
    All rights reserved.

    Redistribution and use in source and binary forms, with or without
    modification, are permitted provided that the following conditions
    are met:
    1. Redistributions of source code must retain the above copyright
       notice, this list of conditions and the following disclaimer
       in this position and unchanged.
    2. Redistributions in binary form must reproduce the above copyright
       notice, this list of conditions and the following disclaimer in the
       documentation and/or other materials provided with the distribution.

    THIS SOFTWARE IS PROVIDED BY THE AUTHOR(S) ``AS IS'' AND ANY EXPRESS OR
    IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE IMPLIED WARRANTIES
    OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE DISCLAIMED.
    IN NO EVENT SHALL THE AUTHOR(S) BE LIABLE FOR ANY DIRECT, INDIRECT,
    INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT
    NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
    DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
    THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
    (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF
    THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

Each archive mixes stored entries and compressed ones (method 0x33 or other),
or holds only stored entries. File names are kept as libarchive has them.

SHA-256 of each file:

```
7d32e2ef511d0ade372345440838165df828197eff4d7816880dafde690cce03  test_read_format_rar_compress_normal.rar
b42c3bdfd96eac9c3ab336b04b3b65d01a26aca099de4fae2b7d77372b83b4cc  test_read_format_rar_noeof.rar
d421b86f6290aefad61b2a36737253b2b30fe27c156bd95abfc230f24fe0307e  test_read_format_rar.rar
e871277670529329cc2c06f178ced453c560d03fd26c76614f42ef9c06b50af0  test_read_format_rar_subblock.rar
3dc3d2a8f3f6cbfeeb6f45b3bb09fa7b2bf4b52e10e80df29217fdeaa9dbda45  test_read_format_rar_unicode.rar
8d689455e9ecd92c19426604e2360b5ef8eb023890fe46aabbe2864260b70fc9  test_read_format_rar_windows.rar
```
