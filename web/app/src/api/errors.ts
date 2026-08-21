export class ApiError extends Error {
  readonly status: number;
  readonly requestID: string | null;

  constructor(status: number, message: string, requestID: string | null) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.requestID = requestID;
  }
}
