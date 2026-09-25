export interface CorpusObject {
  bucket?: string;
  key: string;
  body: string;
  contentType?: string;
  metadata?: Record<string, string>;
}

export interface CorpusPart {
  number: number;
  body: string;
  repeat?: number;
}

export interface CorpusPage {
  contents?: string[];
  commonPrefixes?: string[];
  keyCount: number;
  isTruncated: boolean;
}

export interface CorpusExpectation {
  status: number;
  body?: string;
  contentType?: string;
  metadata?: Record<string, string>;
  errorCode?: string;
  etag?: string;
  bodyLength?: number;
  checksumAlgorithm?: string;
  checksumValue?: string;
  contents?: string[];
  commonPrefixes?: string[];
  keyCount?: number;
  isTruncated?: boolean;
  pages?: CorpusPage[];
  partNumbers?: number[];
}

export interface CorpusCase {
  id: string;
  operation: string;
  bucket: string;
  key?: string;
  body?: string;
  contentType?: string;
  metadata?: Record<string, string>;
  setup?: CorpusObject[];
  expect: CorpusExpectation;
  ifNoneMatch?: string;
  ifMatch?: string;
  contentMD5?: string;
  checksumAlgorithm?: string;
  checksumValue?: string;
  prefix?: string;
  delimiter?: string;
  maxKeys?: number;
  encodingType?: string;
  sourceBucket?: string;
  sourceKey?: string;
  destinationBucket?: string;
  destinationKey?: string;
  metadataDirective?: string;
  copySourceIfMatch?: string;
  copySourceIfNoneMatch?: string;
  parts?: CorpusPart[];
  listMultipartUploads?: boolean;
}

export interface Corpus {
  version: number;
  cases: CorpusCase[];
}
