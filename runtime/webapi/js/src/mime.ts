// MIME type parsing and serialization per the MIME Sniffing Standard.
const httpWS = /^[\t\n\r ]+|[\t\n\r ]+$/g;
const tokenRe = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/;
const quotedOK = /^[\t\x20-\x7e\x80-\xff]*$/;

export interface MIMEType { type: string; subtype: string; params: Map<string, string> }

export function parseMIMEType(input: string): MIMEType | null {
  input = input.replace(httpWS, "");
  let pos = 0;
  const slash = input.indexOf("/");
  if (slash < 0) return null;
  const type = input.slice(0, slash);
  if (!type || !tokenRe.test(type)) return null;
  pos = slash + 1;
  let semi = input.indexOf(";", pos);
  if (semi < 0) semi = input.length;
  const subtype = input.slice(pos, semi).replace(/[\t\n\r ]+$/, "");
  if (!subtype || !tokenRe.test(subtype)) return null;
  const mt: MIMEType = { type: type.toLowerCase(), subtype: subtype.toLowerCase(), params: new Map() };
  pos = semi;
  while (pos < input.length) {
    pos++; // skip ";"
    while (pos < input.length && /[\t\n\r ]/.test(input[pos])) pos++;
    let end = pos;
    while (end < input.length && input[end] !== ";" && input[end] !== "=") end++;
    let name = input.slice(pos, end).toLowerCase();
    pos = end;
    if (pos < input.length) {
      if (input[pos] === ";") continue;
      pos++; // skip "="
    }
    if (pos >= input.length) break;
    let value: string;
    if (input[pos] === '"') {
      // Collect an HTTP quoted string, extracting the value.
      value = "";
      pos++;
      for (;;) {
        while (pos < input.length && input[pos] !== '"' && input[pos] !== "\\") value += input[pos++];
        if (pos >= input.length) break;
        const c = input[pos++];
        if (c === "\\") {
          if (pos >= input.length) { value += "\\"; break; }
          value += input[pos++];
        } else break; // closing quote
      }
      while (pos < input.length && input[pos] !== ";") pos++;
    } else {
      let e2 = input.indexOf(";", pos);
      if (e2 < 0) e2 = input.length;
      value = input.slice(pos, e2).replace(/[\t\n\r ]+$/, "");
      pos = e2;
      if (!value) continue;
    }
    if (name && tokenRe.test(name) && quotedOK.test(value) && !mt.params.has(name)) mt.params.set(name, value);
  }
  return mt;
}

export function serializeMIMEType(mt: MIMEType): string {
  let s = `${mt.type}/${mt.subtype}`;
  for (const [name, value] of mt.params) {
    s += `;${name}=`;
    s += value !== "" && tokenRe.test(value) ? value : '"' + value.replace(/(["\\])/g, "\\$1") + '"';
  }
  return s;
}
