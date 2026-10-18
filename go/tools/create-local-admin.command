#!/usr/bin/env python3
"""Create the owner in the local preview database; keep passwords out of files."""

import getpass
import os
from pathlib import Path
import subprocess
import sys


def main():
    binary = Path(__file__).resolve().parents[1] / "blog"
    if not binary.is_file():
        print("Сначала соберите блог: cd go && go build -o blog ./cmd/blog")
        return 1
    if not sys.stdin.isatty():
        print("Запустите этот скрипт в Терминале для скрытого ввода пароля.")
        return 1

    print("Администратор локального блога: niko@antiit.ru")
    print("Пароль вводится скрыто и не сохраняется в файл.")
    while True:
        password = getpass.getpass("Новый пароль (12–72 байта): ")
        if not 12 <= len(password.encode("utf-8")) <= 72:
            print("Нужно от 12 до 72 байт. Для латиницы это 12–72 символа.")
            continue
        if getpass.getpass("Повторите пароль: ") != password:
            print("Пароли не совпали. Попробуйте ещё раз.")
            continue
        break

    environment = os.environ.copy()
    environment.update(
        DATABASE_URL="postgres://blog@127.0.0.1:55432/blog_test?sslmode=disable",
        OWNER_EMAIL="niko@antiit.ru",
        OWNER_NAME="Денис Николас",
    )
    result = subprocess.run(
        [str(binary), "create-owner"],
        input=password + "\n", text=True, env=environment, capture_output=True,
    )
    del password
    if result.returncode:
        print("Администратор не создан. Проверьте доступность локальной базы и отсутствие такого пользователя.")
        print("Существующий пароль не изменён.")
    else:
        print("Администратор создан. Вход: http://localhost:8080/login/")
    return result.returncode


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (KeyboardInterrupt, EOFError):
        print("\nСоздание отменено.")
        sys.exit(1)
