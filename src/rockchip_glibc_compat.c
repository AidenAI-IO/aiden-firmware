#include <ctype.h>
#include <stdio.h>

/*
 * Rockchip's bundled RV1106 glibc archive was built against an older glibc
 * that exported these internal entry points. Keep the compatibility surface
 * local to the Debian build while the vendor archive remains in use.
 */
const unsigned short int* __ctype_b;

__attribute__((constructor)) static void initialize_legacy_ctype_table(void) {
    __ctype_b = *__ctype_b_loc();
}

int __fputc_unlocked(int character, FILE* stream) {
    return fputc_unlocked(character, stream);
}
