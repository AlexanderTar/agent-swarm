import { type FormEvent, useState } from "react";
import { ApiError, errorText } from "../api";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { C } from "../copy";

function loginError(e: unknown): string {
  if (e instanceof ApiError && e.code === "rate_limited") return C.tooManyAttempts(e.retryAfter);
  if (e instanceof ApiError && e.status === 401) return C.wrongLogin;
  return errorText(e);
}

export function Login({ login, onSignedIn }: { login: (email: string, password: string) => Promise<void>; onSignedIn: () => void }) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");

  async function submit(e: FormEvent) {
    e.preventDefault();
    setPending(true);
    setError("");
    try {
      await login(email, password);
      onSignedIn();
    } catch (err) {
      setError(loginError(err));
    } finally {
      setPending(false);
    }
  }

  return (
    <main className="flex min-h-dvh items-center justify-center p-4">
      <Card className="w-full max-w-sm">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <CardHeader>
            <CardTitle>
              <h1>{C.appTitle}</h1>
            </CardTitle>
            <CardDescription>{C.signInHint}</CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="login-email">{C.email}</Label>
              <Input id="login-email" type="email" autoComplete="username" required value={email} onChange={(e) => setEmail(e.target.value)} />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="login-password">{C.password}</Label>
              <Input id="login-password" type="password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} />
            </div>
            {error && (
              <Alert variant="destructive">
                <AlertDescription>{error}</AlertDescription>
              </Alert>
            )}
          </CardContent>
          <CardFooter>
            <Button type="submit" className="w-full" disabled={pending}>{pending ? C.signingIn : C.signIn}</Button>
          </CardFooter>
        </form>
      </Card>
    </main>
  );
}
