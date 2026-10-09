// node:buffer's constants, which clients size their buffers by
// (@elastic/elasticsearch reads constants.MAX_LENGTH at load time).
import { constants, kMaxLength, kStringMaxLength } from "buffer";

export async function GET() {
  return Response.json({
    maxLength: constants.MAX_LENGTH === kMaxLength && kMaxLength > 0,
    maxStringLength: constants.MAX_STRING_LENGTH === kStringMaxLength ? kStringMaxLength : null,
  });
}
